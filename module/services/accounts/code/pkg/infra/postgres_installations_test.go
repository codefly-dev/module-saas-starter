package infra_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// installFixture seeds an org whose owner is a human principal AND a current org
// admin, plus a least-privilege role permitting (resourceKind, action), then
// installs a solution. It returns the org, the admin owner, the granted role, the
// installation, and the solution's root scope path.
func installFixture(t *testing.T, resourceKind, action string) (orgID, ownerID, roleID string, installation *gen.Installation, rootPath string) {
	t.Helper()
	ownerID = seedUser(t)
	seedHumanPrincipal(t, ownerID, "Installer Admin")
	orgID = seedOrg(t, ownerID)
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, ownerID, "admin"))

	roleID = business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "solution role " + roleID,
			Description: "grants " + resourceKind + ":" + action,
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: resourceKind, Action: action}},
		})
	}))

	rootPath = "sol_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopePath:      rootPath,
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			AllowedAudiences:   []string{"acme.collection"},
			AllowedScopes:      []string{resourceKind},
			GrantedBy:          ownerID,
		})
		return err
	}))
	return orgID, ownerID, roleID, installation, rootPath
}

// registerBoundary registers a descendant node of a solution root and returns its
// node id, standing in for a data boundary an ingest task writes to.
func registerBoundary(t *testing.T, orgID, parentPath, label string) (nodeID, path string) {
	t.Helper()
	path = parentPath + "." + label
	nodeID = business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id:        nodeID,
			OrgId:     orgID,
			ScopePath: path,
			Kind:      "boundary",
			Label:     label,
		})
	}))
	return nodeID, path
}

func getInstallationHealth(t *testing.T, orgID, installationID string) gen.InstallationHealth {
	t.Helper()
	var health gen.InstallationHealth
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		_, health, err = testStore.GetInstallation(ctx, orgID, installationID)
		return err
	}))
	return health
}

func writeScope(kind, action, resourceID string) []business.WorkContextPermission {
	return []business.WorkContextPermission{{ResourceKind: kind, Action: action, ResourceID: resourceID}}
}

// getAgentPrincipal reads an agent under the org tenant floor; a bare pooled read
// sees no principals row because the principals RLS policy filters on
// app.current_org_id.
func getAgentPrincipal(t *testing.T, orgID, identifier string) *business.Principal {
	t.Helper()
	var agent *business.Principal
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		agent, err = testStore.GetAgentPrincipal(ctx, orgID, identifier)
		return err
	}))
	return agent
}

func TestInstallSolutionComposesAndResolvesHealthy(t *testing.T) {
	orgID, ownerID, _, installation, _ := installFixture(t, "doc", "write")

	require.Equal(t, orgID, installation.OrgId)
	require.Equal(t, ownerID, installation.OwnerPrincipalId)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE, installation.Status)
	require.NotEmpty(t, installation.AgentPrincipalId)
	require.NotEmpty(t, installation.RootScopeNodeId)

	// The agent principal was composed with the declared ceiling.
	agent := getAgentPrincipal(t, orgID, "acme.example/solution:1.0.0")
	require.Equal(t, installation.AgentPrincipalId, agent.ID)
	require.Equal(t, []string{"acme.collection"}, agent.AllowedAudiences)
	require.Equal(t, []string{"doc"}, agent.AllowedScopes)

	require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		getInstallationHealth(t, orgID, installation.Id))
}

func TestInstallSolutionIsIdempotentPerSolution(t *testing.T) {
	orgID, ownerID, roleID, first, rootPath := installFixture(t, "doc", "write")

	var second *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		second, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopePath:      rootPath,
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			GrantedBy:          ownerID,
		})
		return err
	}))
	require.Equal(t, first.Id, second.Id, "re-installing an active solution returns the same row")
}

func TestResolveInstallationAuthorityMintsUnderOwnerOfRecordAndAgent(t *testing.T) {
	orgID, ownerID, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	facts, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	require.NoError(t, err)
	require.Equal(t, ownerID, facts.OwnerPrincipalID, "context owner is the owner of record")
	require.NotNil(t, facts.Actor)
	require.Equal(t, installation.AgentPrincipalId, facts.Actor.ID, "actor is the installation agent")
	require.Equal(t, business.PrincipalKindAgent, facts.Actor.Kind)
	require.Positive(t, facts.EffectiveRevision())
}

func TestResolveInstallationAuthorityRefusesScopeOutsideStandingGrant(t *testing.T) {
	orgID, _, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	// A resource kind the standing grant's role does not permit.
	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("secret", "write", boundaryID))
	requireStoreErrorType(t, err, business.ErrTypePermission)

	// A boundary node outside the installation's scope subtree.
	otherOrgPath := "other_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id: business.NewIDString(), OrgId: orgID, ScopePath: otherOrgPath, Kind: "solution", Label: "other",
		})
	}))
	outsideID, _ := registerBoundary(t, orgID, otherOrgPath, "boundary_x")
	_, err = testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", outsideID))
	requireStoreErrorType(t, err, business.ErrTypePermission)
}

func TestResolveInstallationAuthorityFailsClosedOnRevokedGrant(t *testing.T) {
	orgID, _, roleID, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, installation.AgentPrincipalId,
			gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, rootPath, roleID)
	}))

	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	requireStoreErrorType(t, err, business.ErrTypePermission)
	require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_STANDING_GRANT_MISSING,
		getInstallationHealth(t, orgID, installation.Id))
}

func TestResolveInstallationAuthorityFailsClosedOnDisabledAgent(t *testing.T) {
	orgID, _, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, err := testStore.DisableAgentPrincipal(ctx, installation.AgentPrincipalId, "test disable")
		return err
	}))

	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	requireStoreErrorType(t, err, business.ErrTypeNotFound)
	require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_DISABLED,
		getInstallationHealth(t, orgID, installation.Id))
}

func TestResolveInstallationAuthorityFailsClosedWhenNoOwnerIsAdminThenTransferRestores(t *testing.T) {
	orgID, ownerID, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	// Demote the owner of record out of org-admin: no owner or co-owner remains
	// admin, so the next mint must fail closed and health flips.
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key
		_, err := tx.Exec(ctx,
			`UPDATE organization_members SET role = 'member' WHERE org_id = $1 AND user_id = $2`, orgID, ownerID)
		return err
	}))

	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	requireStoreErrorType(t, err, business.ErrTypeNotFound)
	require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER,
		getInstallationHealth(t, orgID, installation.Id))

	// Transfer to a fresh, currently-admin human restores health and minting.
	newOwner := seedUser(t)
	seedHumanPrincipal(t, newOwner, "New Admin")
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, newOwner, "admin"))
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, err := testStore.TransferInstallationOwnership(ctx, orgID, installation.Id, newOwner, nil)
		return err
	}))

	require.Equal(t, gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		getInstallationHealth(t, orgID, installation.Id))
	facts, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	require.NoError(t, err)
	require.Equal(t, newOwner, facts.OwnerPrincipalID)
}

func TestUninstallSolutionReversesCompositionAndIsIdempotent(t *testing.T) {
	orgID, _, _, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	var transitioned bool
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, transitioned2, err := testStore.UninstallSolution(ctx, orgID, installation.Id)
		transitioned = transitioned2
		return err
	}))
	require.True(t, transitioned)

	// The agent is revoked, so the mint fails closed.
	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", boundaryID))
	requireStoreErrorType(t, err, business.ErrTypeNotFound)

	// The agent principal's canonical slot is freed for re-installation.
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, agentErr := testStore.GetAgentPrincipal(ctx, orgID, "acme.example/solution:1.0.0")
		requireStoreErrorType(t, agentErr, business.ErrTypeNotFound)
		return nil
	}))

	// A second uninstall is a no-op (no second audit transition).
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, transitioned2, err := testStore.UninstallSolution(ctx, orgID, installation.Id)
		transitioned = transitioned2
		return err
	}))
	require.False(t, transitioned)
}

func requireStoreErrorType(t *testing.T, err error, want business.StoreErrorType) {
	t.Helper()
	require.Error(t, err)
	var se *business.StoreError
	require.True(t, errors.As(err, &se), "want a StoreError, got %v", err)
	require.Equal(t, want, se.StoreErrorType, "unexpected store error type: %v", err)
}
