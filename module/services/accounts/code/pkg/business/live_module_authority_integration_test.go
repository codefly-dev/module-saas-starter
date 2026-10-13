//go:build !pure

package business_test

import (
	"testing"

	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

// The live authority read, over a REAL installation, through the real store.
//
// This test exists because its absence let a regression through that broke every
// module capability path in production. The read joined
// `installations.agent_principal_id` against the calling module principal —
// two different identity spaces, since an installation's agent principal is
// minted per installation with a fresh uuid while a module principal is a
// deterministic uuid from its declared prefix — so the join never matched and
// every capability answered PermissionDenied. It also ran on whatever pool the
// caller's context carried, which for the adapters is the request pool, where
// RLS hides `installations` entirely.
//
// Every test of the mechanism faked the read as always-current. A fake standing
// in for the thing under test proves the caller and never the thing, which is
// why this one goes through `testStore` against real PostgreSQL.
func TestLiveModuleAuthorityOverARealInstallation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "liveauth@example.com", "liveauth", "LiveAuth Co")
	roleID := seedInstallableRole(t, ctx, orgID)
	targetID, alias := declarePresence(t, "liveauth-solution")

	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/liveauth:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "LiveAuth Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)
	acknowledgeInstallation(t, installation)

	// A module principal, which is NOT the installation's agent principal. The
	// regression assumed these were the same value.
	modulePrincipal := business.ModulePrincipalID(alias)
	require.NotEqual(t, installation.AgentPrincipalId, modulePrincipal,
		"the fixture must exercise the two distinct identity spaces, or it cannot catch the regression")

	// An unsealed caller must still resolve its own organization's explicit
	// registry association. The per-installation agent is a different identity.
	live, err := testStore.LiveModuleAuthority(ctx, modulePrincipal, orgID, "")
	require.NoError(t, err)
	require.Equal(t, installation.Id, live.InstallationID)
	require.Equal(t, uint64(1), live.InstallationRevision)
	_, err = testStore.LiveModuleAuthority(ctx, business.ModulePrincipalID("not-installed"), orgID, "")
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)

	// Naming a LIVE installation resolves it.
	live, err = testStore.LiveModuleAuthority(ctx, modulePrincipal, orgID, installation.Id)
	require.NoError(t, err)
	require.Equal(t, installation.Id, live.InstallationID)

	// Naming an installation in ANOTHER organisation is refused, not resolved —
	// the read is scoped by org as well as by id.
	_, otherOrgID := mustUserAndOrg(t, ctx, "other@example.com", "other", "Other Co")
	_, err = testStore.LiveModuleAuthority(ctx, modulePrincipal, otherOrgID, installation.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive)

	// A malformed caller is refused BEFORE the query, so an empty organisation
	// does not surface as a uuid cast error that reads like an outage and gets
	// retried forever.
	_, err = testStore.LiveModuleAuthority(ctx, modulePrincipal, "", "")
	require.ErrorIs(t, err, business.ErrModuleAuthorityUnreadable)
}

// A revoked installation that the capability names is refused.
//
// Separate from the test above so the refusal is attributable: that one proves
// the read resolves and scopes, this one proves revocation actually lands.
func TestLiveModuleAuthorityRefusesARevokedInstallation(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "revoked@example.com", "revoked", "Revoked Co")
	roleID := seedInstallableRole(t, ctx, orgID)
	targetID, alias := declarePresence(t, "revoked-solution")

	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "acme.example/revoked:1.0.0",
		TargetID:        targetID,
		RootScopeLabel:  "Revoked Solution",
		RoleID:          roleID,
		AllowedScopes:   []string{"doc"},
	})
	require.NoError(t, err)
	acknowledgeInstallation(t, installation)
	modulePrincipal := business.ModulePrincipalID(alias)

	// Live first, so the refusal below is the uninstall and not the fixture.
	_, err = testStore.LiveModuleAuthority(ctx, modulePrincipal, orgID, installation.Id)
	require.NoError(t, err)

	require.NoError(t, testService.UninstallSolution(ctx, adminID, orgID, installation.Id))

	_, err = testStore.LiveModuleAuthority(ctx, modulePrincipal, orgID, installation.Id)
	require.ErrorIs(t, err, business.ErrModuleInstallationInactive,
		"a capability naming an uninstalled solution must be refused on the next request")
}
