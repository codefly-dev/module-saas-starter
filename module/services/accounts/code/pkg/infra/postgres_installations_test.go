package infra_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"accounts/pkg/auth"
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

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		installation, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			AllowedAudiences:   []string{"acme.collection"},
			AllowedScopes:      []string{resourceKind},
			GrantedBy:          ownerID,
		})
		return err
	}))
	// The root path is derived server-side from the created node; read it back so
	// descendant boundaries can be registered beneath the real authority root.
	rootPath = scopeNodePath(t, orgID, installation.RootScopeNodeId)
	return orgID, ownerID, roleID, installation, rootPath
}

// scopeNodePath reads a scope node's ltree path under the org tenant floor.
func scopeNodePath(t *testing.T, orgID, nodeID string) string {
	t.Helper()
	var path string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key
		return tx.QueryRow(ctx,
			`SELECT scope_path::text FROM scope_nodes WHERE id = $1 AND org_id = $2`,
			nodeID, orgID).Scan(&path)
	}))
	return path
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
	orgID, ownerID, roleID, first, _ := installFixture(t, "doc", "write")

	var second *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		second, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			// Same authority envelope as installFixture: an identical re-install
			// reconciles to a no-op and returns the existing row.
			AllowedAudiences: []string{"acme.collection"},
			AllowedScopes:    []string{"doc"},
			GrantedBy:        ownerID,
		})
		return err
	}))
	require.Equal(t, first.Id, second.Id, "re-installing an active solution returns the same row")
}

// A re-install of an active solution that narrows the ceiling must not silently
// return the old row; it fails closed so the change is applied deliberately
// (uninstall + reinstall), not lost.
func TestInstallSolutionRejectsChangedCeilingOnReinstall(t *testing.T) {
	orgID, ownerID, roleID, _, _ := installFixture(t, "doc", "write")

	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, e := testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			// installFixture granted ["doc"]; narrowing to nothing must be rejected.
			AllowedAudiences: []string{"acme.collection"},
			AllowedScopes:    nil,
			GrantedBy:        ownerID,
		})
		return e
	})
	requireStoreErrorType(t, err, business.ErrTypeConflict)
}

// A re-install of an active solution whose standing grant an admin has revoked
// out from under it must fail closed with a conflict, not an opaque Internal error
// from the reconciliation's role lookup finding no grant.
func TestInstallSolutionReinstallWithRevokedStandingGrantConflicts(t *testing.T) {
	orgID, ownerID, roleID, installation, rootPath := installFixture(t, "doc", "write")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, installation.AgentPrincipalId,
			gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, rootPath, roleID)
	}))

	err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, e := testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:1.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			AllowedAudiences:   []string{"acme.collection"},
			AllowedScopes:      []string{"doc"},
			GrantedBy:          ownerID,
		})
		return e
	})
	requireStoreErrorType(t, err, business.ErrTypeConflict)
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

// An installation-minted Work Context must survive the consumer-side revocation
// seam (CheckWorkContextAuthorizationRevision), which resolves an actor's
// authority — here it must honor the agent's standing scope_grant, not only flat
// role_assignments, or every valid headless token would be rejected as stale. And
// revoking that grant must flip the recheck to fail closed.
func TestInstallationTokenRevalidatesThroughConsumerRevisionSeam(t *testing.T) {
	orgID, ownerID, roleID, installation, rootPath := installFixture(t, "doc", "write")
	boundaryID, _ := registerBoundary(t, orgID, rootPath, "boundary_a")

	perms := writeScope("doc", "write", boundaryID)
	facts, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id, perms)
	require.NoError(t, err)
	sealed := facts.EffectiveRevision()

	subjects := []business.WorkContextRevisionSubject{
		{PrincipalID: ownerID, Permissions: perms},
		{PrincipalID: installation.AgentPrincipalId, Permissions: perms},
	}
	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	require.NoError(t, testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, sealed, subjects),
		"an installation-minted token must revalidate through the consumer revision seam")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RevokeScope(ctx, orgID, installation.AgentPrincipalId,
			gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, rootPath, roleID)
	}))
	err = testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, sealed, subjects)
	require.ErrorIs(t, err, business.ErrWorkContextAuthorizationStale,
		"revoking the standing grant must flip the recheck to fail closed")
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

// registerSiblingSolutionNode registers a depth-1 solution node OUTSIDE the
// installation root and grants the agent roleID at it, then returns a boundary
// under it. The agent thus holds a genuine ancestor scope_grant covering the
// boundary — the case migration 112's root promise must still deny.
func registerSiblingSolutionNode(t *testing.T, orgID, agentID, roleID string) (siblingBoundaryID string) {
	t.Helper()
	siblingPath := "sib_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.RegisterScopeNode(ctx, &gen.ScopeNode{
			Id: business.NewIDString(), OrgId: orgID, ScopePath: siblingPath, Kind: "solution", Label: "sibling",
		})
	}))
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return testStore.GrantScope(ctx, &gen.ScopeGrant{
			Id: business.NewIDString(), OrgId: orgID,
			SubjectId: agentID, SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
			ScopePath: siblingPath, RoleId: roleID,
		})
	}))
	siblingBoundaryID, _ = registerBoundary(t, orgID, siblingPath, "boundary_b")
	return siblingBoundaryID
}

// The requested boundary must fall within the installation's own root even when
// the agent later holds an ancestor grant elsewhere. A grant at a sibling node
// (a record share, another install's node, an admin's ad-hoc GrantScope) must not
// widen what the headless mint can reach; only descendants of the root are minted.
func TestResolveInstallationAuthorityBoundsRequestedBoundaryToRoot(t *testing.T) {
	orgID, _, roleID, installation, rootPath := installFixture(t, "doc", "write")

	// A descendant of the installation root is minted.
	inRoot, _ := registerBoundary(t, orgID, rootPath, "boundary_a")
	_, err := testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", inRoot))
	require.NoError(t, err)

	// A sibling node the agent ALSO holds a doc:write grant at, but outside the
	// root, is denied: the ancestor-grant check alone would allow it.
	sibling := registerSiblingSolutionNode(t, orgID, installation.AgentPrincipalId, roleID)
	_, err = testStore.ResolveInstallationAuthority(testCtx, orgID, installation.Id,
		writeScope("doc", "write", sibling))
	requireStoreErrorType(t, err, business.ErrTypePermission)
}

// An outstanding token that names a sibling-node boundary must fail the consumer
// revision seam too, not only the mint — the mint and recheck apply the same root
// bound, so a token can never be revalidated for a boundary it could not be minted
// for.
func TestInstallationTokenRecheckBoundsBoundaryToRoot(t *testing.T) {
	orgID, ownerID, roleID, installation, _ := installFixture(t, "doc", "write")
	sibling := registerSiblingSolutionNode(t, orgID, installation.AgentPrincipalId, roleID)

	perms := writeScope("doc", "write", sibling)
	verified := auth.WithVerifiedDatabaseIdentity(testCtx, ownerID, orgID)
	subjects := []business.WorkContextRevisionSubject{
		{PrincipalID: installation.AgentPrincipalId, Permissions: perms},
	}
	err := testStore.CheckWorkContextAuthorizationRevision(verified, orgID, ownerID, 1, subjects)
	require.ErrorIs(t, err, business.ErrWorkContextAuthorizationStale,
		"a token for a boundary outside the installation root must fail recheck")
}

// A solution reinstalled after uninstall reuses the authority-root node its prior
// install left behind, so the root path — and any boundaries registered under it —
// stay stable across the reinstall.
func TestInstallSolutionReusesRootNodeOnReinstall(t *testing.T) {
	orgID, ownerID, roleID, first, firstRoot := installFixture(t, "doc", "write")

	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, _, err := testStore.UninstallSolution(ctx, orgID, first.Id)
		return err
	}))

	var second *gen.Installation
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		second, err = testStore.InstallSolution(ctx, &business.InstallSolutionParams{
			OrgID:              orgID,
			AgentIdentifier:    "acme.example/solution:2.0.0",
			SolutionIdentifier: "acme.example/solution",
			RootScopeLabel:     "Acme Solution",
			RoleID:             roleID,
			OwnerPrincipalID:   ownerID,
			GrantedBy:          ownerID,
		})
		return err
	}))
	require.Equal(t, first.RootScopeNodeId, second.RootScopeNodeId, "reinstall reuses the prior solution node")
	require.Equal(t, firstRoot, scopeNodePath(t, orgID, second.RootScopeNodeId))
}

func requireStoreErrorType(t *testing.T, err error, want business.StoreErrorType) {
	t.Helper()
	require.Error(t, err)
	var se *business.StoreError
	require.True(t, errors.As(err, &se), "want a StoreError, got %v", err)
	require.Equal(t, want, se.StoreErrorType, "unexpected store error type: %v", err)
}
