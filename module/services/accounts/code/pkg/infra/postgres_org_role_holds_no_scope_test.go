//go:build !pure

package infra_test

import (
	"testing"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// The membership role "owner" or "admin" names who administers an
// organization. It is not a permission: in the legacy Work Context mint an
// owner or admin with no grant holds nothing, where a person holding a grant
// holds exactly it. Before this test's change the first arm of the permission
// decision was the role name, so the owner below was allowed everything.
func TestAnOrganizationAdministratorHoldsOnlyWhatIsGranted(t *testing.T) {
	for _, role := range []string{"owner", "admin"} {
		t.Run(role, func(t *testing.T) {
			personID := seedUser(t)
			orgID := seedOrg(t, personID)
			require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, personID, role))
			verified := auth.WithVerifiedDatabaseIdentity(testCtx, personID, orgID)
			ask := func(kind, action string) error {
				_, err := testStore.ResolveWorkContextAuthority(verified, orgID, personID, "", writeScope(kind, action, ""))
				return err
			}

			err := ask("ledger.entry", "write")
			require.Error(t, err, "an %s with no grant is issued nothing", role)
			var denied *business.StoreError
			require.ErrorAs(t, err, &denied)
			require.Equal(t, business.ErrTypePermission, denied.StoreErrorType)
			require.Contains(t, err.Error(), "ledger.entry:write", "the refusal names what was asked")

			seedScopedRole(t, orgID, personID, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "ledger.entry", "read", "")
			require.NoError(t, ask("ledger.entry", "read"), "a grant is held")
			require.Error(t, ask("ledger.entry", "write"), "and only the grant is held")
			require.Error(t, ask("other.kind", "read"), "on only its kind")
		})
	}
}

// The owner of an installation answers for the installation's agent and for
// nobody else's, and never for itself: the agent's standing grants bound what
// is revalidated, and the owner of record cannot claim them as its own.
func TestAnInstallationOwnerOfRecordClaimsNothingForItself(t *testing.T) {
	orgID, ownerID, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_owner_claim")
	perms := writeScope("doc", "write", boundaryID)
	facts, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id, perms)
	require.NoError(t, err)
	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)

	require.NoError(t, testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, facts.EffectiveRevision(),
		[]business.WorkContextRevisionSubject{{PrincipalID: installation.AgentPrincipalId, Permissions: perms}}),
		"the installation's own agent revalidates for its owner of record")

	alone := testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, facts.EffectiveRevision(),
		[]business.WorkContextRevisionSubject{{PrincipalID: ownerID, Permissions: perms}})
	require.ErrorIs(t, alone, business.ErrWorkContextAuthorizationStale, "the owner of record holds no doc:write of its own")

	wider := testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, facts.EffectiveRevision(), []business.WorkContextRevisionSubject{
		{PrincipalID: ownerID, Permissions: append(writeScope("doc", "delete", boundaryID), perms...)},
		{PrincipalID: installation.AgentPrincipalId, Permissions: perms},
	})
	require.ErrorIs(t, wider, business.ErrWorkContextAuthorizationStale, "the owner's slice is held to what its agent holds")

	stranger := seedUser(t)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, stranger, "admin"))
	err = testStore.CheckWorkContextAuthorizationRevision(auth.WithVerifiedDatabaseIdentity(testCtx, stranger, orgID), orgID, stranger, facts.EffectiveRevision(),
		[]business.WorkContextRevisionSubject{{PrincipalID: installation.AgentPrincipalId, Permissions: perms}})
	require.ErrorIs(t, err, business.ErrWorkContextAuthorizationStale, "an administrator who owns no such installation does not answer for its agent")
}
