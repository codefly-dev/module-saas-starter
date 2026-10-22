package adapters

import (
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A grant of a role that only reads — a collection shared with a team — is
// not the privilege SP-IDENT-10 protects: the subject can change nothing. The
// factor gate therefore lets it through for a privileged actor with no recent
// factor, while the same actor granting a role with any write is still refused.
// What is exempted is what the store says the role grants, never its name, and
// a role the store does not know is treated as privilege-granting.
func TestReadOnlyRoleGrantNeedsNoRecentFactor(t *testing.T) {
	readOnly := &gen.Role{Id: factorUUID, OrgId: mfaOrgID, Name: "collection readers",
		Permissions: []*gen.Permission{{Resource: "collection.content", Action: "read"}}}
	writes := &gen.Role{Id: "00000000-0000-4000-8000-0000000000c2", OrgId: mfaOrgID, Name: "collection editors",
		Permissions: []*gen.Permission{{Resource: "collection.content", Action: "read"}, {Resource: "collection.content", Action: "write"}}}
	installMFAPolicyStore(t, &mfaPolicyStore{platformRole: "super_admin", roles: []*gen.Role{readOnly, writes}})
	ctx := privilegedActorContext()
	server := &PermServer{}

	// The read-only grant stands aside from the gate. Asserted on the decision
	// the handler takes rather than through the handler: past the gate the
	// grant proceeds into the service, and this package's fixture store serves
	// no mutation.
	exempt, err := roleGrantsOnlyReads(ctx, mfaOrgID, readOnly.Id)
	require.NoError(t, err)
	require.True(t, exempt, "a role that only reads needs no recent factor")
	for name, roleID := range map[string]string{"a role with a write": writes.Id, "an unknown role": "00000000-0000-4000-8000-0000000000c3"} {
		exempt, err := roleGrantsOnlyReads(ctx, mfaOrgID, roleID)
		require.NoError(t, err, name)
		require.False(t, exempt, name)
	}

	for name, roleID := range map[string]string{"a role with a write": writes.Id, "an unknown role": "00000000-0000-4000-8000-0000000000c3"} {
		t.Run(name, func(t *testing.T) {
			_, err := server.GrantScope(ctx, &gen.GrantScopeRequest{OrgId: mfaOrgID, ScopePath: "/collections/wiki",
				RoleId: roleID, SubjectId: factorUUID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_TEAM})
			require.Error(t, err)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Equal(t, "mfa_required", status.Convert(err).Message())
		})
	}
}
