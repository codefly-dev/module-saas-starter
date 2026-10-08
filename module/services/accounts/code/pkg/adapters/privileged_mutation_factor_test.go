package adapters

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SP-IDENT-10: a platform administrator or an organization owner carries out a
// sensitive operation only behind a recent second factor.
//
// requireMFA was made fail-closed for a privileged actor, but the privilege-granting
// and platform-security mutations did not reach it: each ran its authorization guard
// and then its mutation. A helper nothing calls enforces nothing.
func TestR1019PrivilegedMutationRequiresFactor(t *testing.T) {
	// A super-admin with nothing enrolled and no recent factor. requireRoleScope
	// falls to the platform role when no organization is named, so both servers'
	// authorization guards pass and the factor gate is what answers.
	for _, grade := range []string{"support", "billing", "super_admin"} {
		t.Run(grade, func(t *testing.T) {
			installMFAPolicyStore(t, &mfaPolicyStore{platformRole: grade})
			ctx := privilegedActorContext()

			// Counted, not assumed: a grade below every method's role floor would be
			// refused by the authorization guard on every call and prove nothing
			// about the factor gate. At least one call per grade has to reach it.
			reachedTheGate := 0
			for _, call := range privilegedMutations() {
				err := call.invoke(ctx)
				require.Error(t, err, "%s must not mutate without a recent factor", call.name)
				if status.Code(err) == codes.PermissionDenied {
					// This grade is below the method's role floor, so the
					// authorization guard answered first — a stronger refusal, and
					// not what this test is about.
					continue
				}
				require.Equal(t, codes.FailedPrecondition, status.Code(err),
					"%s: expected the factor gate to answer", call.name)
				require.Equal(t, "mfa_required", status.Convert(err).Message(), call.name)
				reachedTheGate++
			}
			require.NotZero(t, reachedTheGate,
				"every call was refused before the factor gate, so this grade proved nothing")
		})
	}
}

// The control: with recent AAL2 evidence the same privileged actor passes the gate,
// so the refusals above are the missing factor and not a handler that refuses
// everything. Asserted on the gate itself rather than through a handler, because a
// handler that passes the gate proceeds into the service and this package's fixtures
// wire no store for the mutations themselves.
func TestR1019PrivilegedMutationAdmittedWithRecentFactor(t *testing.T) {
	installMFAPolicyStore(t, &mfaPolicyStore{platformRole: "super_admin"})
	ctx := stampVerifiedIdentity(context.Background(), mfaActorID, "", auth.Assurance{
		Level:                 auth.AssuranceLevelAAL2,
		AuthenticationMethods: []string{auth.AuthenticationMethodWebAuthn},
		MFAVerifiedAt:         time.Now(),
	})
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, mfaActorID, mfaOrgID)

	require.NoError(t, requireMFA(ctx, mfaActorID),
		"recent AAL2 evidence must satisfy the gate these mutations now carry")
}

// Every method listed here must carry the gate, and carry it BEFORE its mutation: a
// factor check the write has already passed is not a check. Read from the source, so
// a method added to the privileged set cannot be left ungated, and so the ordering
// cannot regress to the shape this finding started as.
func TestR1019PrivilegedMutationsGateBeforeTheMutation(t *testing.T) {
	source, err := os.ReadFile("rpcs.go")
	require.NoError(t, err)
	text := string(source)

	for _, m := range privilegedMutationMethods {
		pattern := regexp.MustCompile(`func \(s \*` + m.server + `\) ` + m.method +
			`\(ctx context\.Context(?s:.*?)\n\}\n`)
		body := pattern.FindString(text)
		require.NotEmpty(t, body, "%s.%s not found", m.server, m.method)

		gate := strings.Index(body, "requireMFA(")
		require.NotEqual(t, -1, gate,
			"%s.%s is a privileged mutation and must gate a second factor", m.server, m.method)
		mutation := strings.Index(body, "service.")
		require.NotEqual(t, -1, mutation, "%s.%s calls no service method", m.server, m.method)
		require.Less(t, gate, mutation,
			"%s.%s gates the factor AFTER its mutation, which enforces nothing",
			m.server, m.method)
	}
}

type privilegedMutation struct {
	server, method string
}

// The privilege-granting and platform-security mutations. Roles, scopes and
// principals grant authority; the platform-admin entries change another identity's
// state or a tenant's ceiling.
//
// UpsertFeatureFlag is deliberately absent: it mutates nothing, returning
// FailedPrecondition for a retired inventory.
var privilegedMutationMethods = []privilegedMutation{
	{"PermServer", "CreateRole"}, {"PermServer", "UpdateRole"}, {"PermServer", "DeleteRole"},
	{"PermServer", "AssignRole"}, {"PermServer", "RevokeRole"},
	{"PermServer", "GrantScope"}, {"PermServer", "RevokeScope"},
	{"PlatformAdminServer", "SuspendUser"}, {"PlatformAdminServer", "UnsuspendUser"},
	{"PlatformAdminServer", "RevokeSession"}, {"PlatformAdminServer", "OverrideEntitlement"},
}

type factorCall struct {
	name   string
	invoke func(context.Context) error
}

func privilegedMutations() []factorCall {
	perm := &PermServer{}
	admin := &PlatformAdminServer{}
	return []factorCall{
		{"AssignRole", func(ctx context.Context) error {
			_, err := perm.AssignRole(ctx, &gen.AssignRoleRequest{
				RoleId: factorUUID, SubjectId: factorUUID,
				SubjectKind: gen.SubjectKind_SUBJECT_KIND_USER,
			})
			return err
		}},
		{"RevokeRole", func(ctx context.Context) error {
			_, err := perm.RevokeRole(ctx, &gen.RevokeRoleRequest{
				RoleId: factorUUID, SubjectId: factorUUID,
			})
			return err
		}},
		{"SuspendUser", func(ctx context.Context) error {
			_, err := admin.SuspendUser(ctx, &gen.SuspendUserRequest{
				UserId: factorUUID, Reason: "a recorded reason",
			})
			return err
		}},
		{"OverrideEntitlement", func(ctx context.Context) error {
			_, err := admin.OverrideEntitlement(ctx, &gen.OverrideEntitlementRequest{
				OrgId: factorUUID, Feature: "seats", LimitValue: 10,
				Reason: "a recorded reason",
			})
			return err
		}},
	}
}

func privilegedActorContext() context.Context {
	ctx := stampVerifiedIdentity(context.Background(), mfaActorID, "", auth.Assurance{})
	return auth.WithVerifiedDatabaseIdentity(ctx, mfaActorID, mfaOrgID)
}

const factorUUID = "00000000-0000-4000-8000-0000000000d1"
