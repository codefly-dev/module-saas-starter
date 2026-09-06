package business_test

import (
	"context"
	"strings"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/stretchr/testify/require"
)

// TestInstallSolutionEmitsAuditWithGrantorAndCeiling exercises the full business
// path: an org admin installs a solution and the composition is recorded as a
// single installation.created audit event carrying the grantor, the agent
// principal, and the granted role — the accountability the acceptance criteria
// require.
func TestInstallSolutionEmitsAuditWithGrantorAndCeiling(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "installer@example.com", "installer", "Installer Co")

	roleID := business.NewIDString()
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return testStore.CreateRole(ctx, &gen.Role{
			Id:          roleID,
			Name:        "solution role " + roleID,
			Description: "least-privilege contributed role",
			OrgId:       orgID,
			Permissions: []*gen.Permission{{Resource: "doc", Action: "write"}},
		})
	}))

	rootPath := "sol_" + strings.ReplaceAll(business.NewIDString(), "-", "_")
	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:              orgID,
		AgentIdentifier:    "acme.example/solution:1.0.0",
		SolutionIdentifier: "acme.example/solution",
		RootScopePath:      rootPath,
		RootScopeLabel:     "Acme Solution",
		RoleID:             roleID,
		AllowedAudiences:   []string{"acme.collection"},
		AllowedScopes:      []string{"doc"},
	})
	require.NoError(t, err)
	require.Equal(t, adminID, installation.OwnerPrincipalId, "owner of record defaults to the installing admin")

	events, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID:     orgID,
		EventType: string(business.EventInstallationCreated),
		PageSize:  10,
	})
	require.NoError(t, err)
	require.Len(t, events, 1, "install must emit exactly one installation.created event")
	entry := events[0]
	require.Equal(t, installation.Id, entry.ResourceID)
	require.Equal(t, adminID, entry.ActorID, "the grantor is on the audit event")
	require.Equal(t, installation.AgentPrincipalId, entry.Payload["agent_principal_id"])
	require.Equal(t, roleID, entry.Payload["role_id"])
}

// A malformed agent identifier is a boundary input: it must be rejected as a
// validation error, not surface as an opaque Internal from a DB CHECK violation.
// Store-level composition skips business.Principal.Validate(), so InstallSolution
// validates the shape itself.
func TestInstallSolutionRejectsMalformedAgentIdentifier(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "badagent@example.com", "badagent", "Bad Agent Co")

	_, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:              orgID,
		AgentIdentifier:    "notacanonicalidentifier", // no '/' or ':'
		SolutionIdentifier: "acme.example/solution",
		RootScopePath:      "sol_bad",
		RoleID:             business.NewIDString(),
	})
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeValidation, se.StoreErrorType)
}
