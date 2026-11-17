package adapters

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// permissionStore is enforceStore plus the role grants CheckPermission reads:
// `granted` holds the "resource:action" pairs the actor's role assignments
// carry in the organization.
type permissionStore struct {
	*enforceStore
	granted map[string]bool
}

func (s *permissionStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *permissionStore) CheckPermission(_ context.Context, _ string, _ gen.SubjectKind, resource, action, _, _ string) (bool, string, error) {
	if s.granted[resource+":"+action] {
		return true, "granted via role: test", nil
	}
	return false, "no matching permission found", nil
}

func permissionMemberStore(role gen.OrgRole, granted ...string) *permissionStore {
	store := &permissionStore{enforceStore: memberStore(role), granted: map[string]bool{}}
	for _, permission := range granted {
		store.granted[permission] = true
	}
	return store
}

// The verified finding: a member of the organization holding no role read its
// audit trail, its API-key inventory and its webhook deliveries, because a
// method that declared a permission was admitted on membership alone. Each of
// these declares ORG_MEMBER plus a permission the vocabulary gives to the
// administrator.
var memberReadableByDeclaredPermission = map[string]string{
	"/saas.accounts.v1.APIKeyService/ListAPIKeys":        "api_keys:read",
	"/saas.accounts.v1.WebhookService/ListSubscriptions": "webhooks:read",
	"/saas.accounts.v1.WebhookService/ListDeliveries":    "webhooks:read",
	"/saas.accounts.v1.WebhookService/GetDelivery":       "webhooks:read",
}

func TestAMemberWithoutTheDeclaredPermissionIsRefusedAtAdmission(t *testing.T) {
	for method, permission := range memberReadableByDeclaredPermission {
		t.Run(strings.TrimPrefix(method, "/saas.accounts.v1."), func(t *testing.T) {
			t.Run("an administrator is admitted", func(t *testing.T) {
				installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_ADMIN))
				require.NoError(t, enforceCentralPolicy(enforceActorCtx(), method))
			})
			t.Run("a member holding no role is refused", func(t *testing.T) {
				installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_MEMBER))
				err := enforceCentralPolicy(enforceActorCtx(), method)
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Contains(t, status.Convert(err).Message(), permission)
			})
			t.Run("a member whose role carries the permission is admitted", func(t *testing.T) {
				installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_MEMBER, permission))
				require.NoError(t, enforceCentralPolicy(enforceActorCtx(), method))
			})
			t.Run("someone outside the organization is refused", func(t *testing.T) {
				installEnforceService(t, &permissionStore{enforceStore: &enforceStore{}, granted: map[string]bool{permission: true}})
				err := enforceCentralPolicy(enforceActorCtx(), method)
				require.Equal(t, codes.PermissionDenied, status.Code(err))
			})
		})
	}
}

// Every method that declares a permission either has it enforced at admission
// or names the one handler rule that decides it. A new method declaring a
// permission lands in the first set by default, so the shape that was open
// cannot come back by omission.
func TestEveryDeclaredPermissionIsEnforcedOrNamesItsRule(t *testing.T) {
	for _, policy := range business.RPCPolicies() {
		declared := policy.MethodPolicy.GetPermissions()
		if len(declared) == 0 {
			continue
		}
		t.Run(strings.TrimPrefix(policy.FullMethod, "/saas.accounts.v1."), func(t *testing.T) {
			enforced, central := business.CentralPermissionEnforcement(policy)
			if !central {
				require.NotEmptyf(t, business.PermissionDecidedByHandler(policy.FullMethod),
					"%s declares %v and neither admission nor a named handler rule enforces it", policy.FullMethod, declared)
				return
			}
			require.Equal(t, declared, enforced)
			installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_MEMBER))
			err := enforceCentralPolicy(enforceActorCtx(), policy.FullMethod)
			require.Equalf(t, codes.PermissionDenied, status.Code(err),
				"%s admitted a member who holds none of %v", policy.FullMethod, declared)
		})
	}
}

// The audit trail is read per organization or, with no organization named,
// platform-wide by a platform role. Admission cannot tell the two apart (it
// never reads the request), so the handlers decide audit:read, through the same
// helper admission uses.
func TestTheAuditTrailNeedsAuditRead(t *testing.T) {
	query := func(ctx context.Context) error {
		_, err := (&AuditServer{}).QueryAuditLog(ctx, &gen.QueryAuditLogRequest{OrgId: enforceOrgID, PageSize: 5})
		return err
	}
	queryOwnOrganization := func(ctx context.Context) error {
		_, err := (&AuditServer{}).QueryAuditLog(ctx, &gen.QueryAuditLogRequest{PageSize: 5})
		return err
	}
	aggregate := func(ctx context.Context) error {
		_, err := (&AuditServer{}).AggregateAuditLog(ctx, &gen.AggregateAuditLogRequest{OrgId: enforceOrgID})
		return err
	}
	eventTypes := func(ctx context.Context) error {
		_, err := (&AuditServer{}).ListAuditEventTypes(ctx, &gen.ListAuditEventTypesRequest{})
		return err
	}
	for name, call := range map[string]func(context.Context) error{
		"QueryAuditLog":                   query,
		"QueryAuditLog, own organization": queryOwnOrganization,
		"AggregateAuditLog":               aggregate,
		"ListAuditEventTypes":             eventTypes,
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("a member holding no role is refused", func(t *testing.T) {
				installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_MEMBER))
				err := callRecovering(call, orgScopedCtx())
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Contains(t, status.Convert(err).Message(), "audit:read")
			})
			t.Run("someone outside the organization is refused", func(t *testing.T) {
				installEnforceService(t, &permissionStore{enforceStore: &enforceStore{}, granted: map[string]bool{}})
				require.Equal(t, codes.PermissionDenied, status.Code(callRecovering(call, orgScopedCtx())))
			})
			t.Run("an administrator passes the permission", func(t *testing.T) {
				installEnforceService(t, permissionMemberStore(gen.OrgRole_ORG_ROLE_ADMIN))
				// The store behind this test answers no audit query, so the
				// call fails past authorization; what is asserted is that the
				// refusal is not the permission's.
				err := callRecovering(call, orgScopedCtx())
				require.NotEqual(t, codes.PermissionDenied, status.Code(err))
			})
		})
	}
}

// orgScopedCtx is the enforce actor's verified identity. Stamping it binds the
// organization on the request context too, where a handler defaulting to "the
// caller's own organization" reads it.
func orgScopedCtx() context.Context { return enforceActorCtx() }

// callRecovering runs call and turns a nil-store panic past the authorization
// guards into a non-authorization error.
func callRecovering(call func(context.Context) error, ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			err = status.Error(codes.Unavailable, "reached the store")
		}
	}()
	return call(ctx)
}
