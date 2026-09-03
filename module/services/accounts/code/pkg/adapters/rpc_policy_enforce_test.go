package adapters

import (
	"context"
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	enforceActorID = "019f6bf7-5b1c-730d-9687-fe6d4aff31ed"
	enforceOrgID   = "019f6bf7-5b4b-74e5-8c17-092259bb1661"

	orgAdminMethod    = "/saas.accounts.v1.OrganizationService/AddMember"   // ORG_ADMIN, no permission
	orgMemberMethod   = "/saas.accounts.v1.OrganizationService/ListMembers" // ORG_MEMBER, no permission
	orgPermissionMeth = "/saas.accounts.v1.APIKeyService/CreateAPIKey"      // ORG_ADMIN + api_keys:write
	ownedResourceMeth = "/saas.accounts.v1.InvitationService/RevokeInvitation"
)

type enforceStore struct {
	business.Store
	members      map[string][]*gen.OrgMembership
	platformRole string
}

func (s *enforceStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	for _, member := range s.members[orgID] {
		if member.UserId == userID {
			return member, nil
		}
	}
	return nil, nil
}

func (s *enforceStore) GetPlatformRole(context.Context, string) (string, error) {
	return s.platformRole, nil
}

func installEnforceService(t *testing.T, store business.Store) {
	t.Helper()
	previous := service
	svc, err := business.NewService(store)
	require.NoError(t, err)
	service = svc
	t.Cleanup(func() { service = previous })
}

func enableEnforcement(t *testing.T) {
	t.Helper()
	previous := centralEnforcement
	centralEnforcement = true
	t.Cleanup(func() { centralEnforcement = previous })
}

func memberStore(role gen.OrgRole) *enforceStore {
	return &enforceStore{members: map[string][]*gen.OrgMembership{
		enforceOrgID: {{UserId: enforceActorID, Role: role}},
	}}
}

func enforceActorCtx() context.Context {
	return stampVerifiedIdentity(context.Background(), enforceActorID, enforceOrgID, auth.Assurance{})
}

// Shadow is the fallback: with enforcement off the interceptor changes no
// admission decision even for an org-admin method and a caller who is not a
// member — the handler require* site remains the sole gate.
func TestShadowModeDoesNotDenyAtAdmission(t *testing.T) {
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), orgAdminMethod))
}

func TestEnforceAdmitsOrgAdmin(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), orgAdminMethod))
}

func TestEnforceDeniesNonMemberOnOrgAdminMethod(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, &enforceStore{})
	err := enforceCentralPolicy(enforceActorCtx(), orgAdminMethod)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// A plain member fails the org-admin floor: the interceptor's helper is the same
// requireOrgAdmin the handler runs, so the two never diverge.
func TestEnforceDeniesMemberOnOrgAdminMethod(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
	err := enforceCentralPolicy(enforceActorCtx(), orgAdminMethod)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestEnforceAdmitsOrgMemberOnMemberMethod(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_MEMBER))
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), orgMemberMethod))
}

// A method that declares an org permission on top of its tenant is deferred to
// the handler: the permission may admit a non-admin who holds it, so central
// enforcement must not deny it. A store with no membership would deny if the
// interceptor evaluated the tenant, so the pass proves it deferred.
func TestEnforceDefersPermissionScopedMethod(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, &enforceStore{})
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), orgPermissionMeth))
}

// Owned-resource methods stay handler-enforced until an ownership resolver
// exists; central enforcement defers them rather than denying.
func TestEnforceDefersOwnedResourceMethod(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, &enforceStore{})
	require.NoError(t, enforceCentralPolicy(enforceActorCtx(), ownedResourceMeth))
}

// A caller with no verified organization cannot satisfy an org-scoped floor.
func TestEnforceDeniesWithoutVerifiedOrg(t *testing.T) {
	enableEnforcement(t)
	installEnforceService(t, memberStore(gen.OrgRole_ORG_ROLE_ADMIN))
	ctx := stampVerifiedIdentity(context.Background(), enforceActorID, "", auth.Assurance{})
	err := enforceCentralPolicy(ctx, orgAdminMethod)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
