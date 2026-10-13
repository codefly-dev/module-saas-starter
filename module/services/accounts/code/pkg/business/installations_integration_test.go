//go:build !pure

package business_test

import (
	"context"
	"testing"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

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

	targetID, _ := declarePresence(t, "acme-solution")
	// The ceiling may only name audiences this host SERVES (issue #952): an
	// audience outside the derived vocabulary is refused at install, because a
	// ceiling naming a consumer that does not exist grants a capability pointing
	// nowhere. `acme.collection` is a declared MODULE prefix, which is the set's
	// member a forwarded viewer context names — declared here the way a
	// composition declares it, through the principal registry.
	previousPrincipals := testService.ModulePrincipals()
	registry := testService.ModulePrincipals()
	registry[business.ModulePrincipalID("acme.collection")] = business.ModulePrincipalGrant{Prefix: "acme.collection"}
	testService.SetModulePrincipals(registry)
	t.Cleanup(func() { testService.SetModulePrincipals(previousPrincipals) })

	installation, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:            orgID,
		AgentIdentifier:  "acme.example/solution:1.0.0",
		TargetID:         targetID,
		RootScopeLabel:   "Acme Solution",
		RoleID:           roleID,
		AllowedAudiences: []string{"acme.collection"},
		AllowedScopes:    []string{"doc"},
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
	require.Equal(t, targetID, entry.Payload["target_id"],
		"the audit trail names the immutable target, not a reusable route alias")

	// HOST-INST-002, HOST-INST-005: the business mutation returns pending;
	// retrying failed consent records one audit fact and keeps its revision.
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, installation.Status)
	require.NoError(t, testStore.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE installations SET status='failed',failure_reason='Example refusal' WHERE org_id=$1 AND id=$2`, orgID, installation.Id)
		return err
	}))
	retried, err := testService.RetryInstallation(ctx, adminID, orgID, installation.Id)
	require.NoError(t, err)
	require.Equal(t, gen.InstallationStatus_INSTALLATION_STATUS_PENDING, retried.Status)
	require.Empty(t, retried.FailureReason)
	_, err = testService.RetryInstallation(ctx, adminID, orgID, installation.Id)
	require.Error(t, err)
	events, _, _, err = testService.QueryAuditLog(ctx, business.AuditQuery{OrgID: orgID, EventType: string(business.EventInstallationRetried), PageSize: 10})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "1", events[0].Payload["revision"])
	require.Equal(t, adminID, events[0].ActorID)
	require.Equal(t, installation.Id, events[0].ResourceID)
}

// A malformed agent identifier is a boundary input: it must be rejected as a
// validation error, not surface as an opaque Internal from a DB CHECK violation.
// Store-level composition skips business.Principal.Validate(), so InstallSolution
// validates the shape itself.
func TestInstallSolutionRejectsMalformedAgentIdentifier(t *testing.T) {
	clearData(t)
	ctx := testCtx
	adminID, orgID := mustUserAndOrg(t, ctx, "badagent@example.com", "badagent", "Bad Agent Co")

	targetID, _ := declarePresence(t, "acme-solution")
	_, err := testService.InstallSolution(ctx, adminID, &business.InstallSolutionParams{
		OrgID:           orgID,
		AgentIdentifier: "notacanonicalidentifier", // no '/' or ':'
		TargetID:        targetID,
		RoleID:          business.NewIDString(),
	})
	require.Error(t, err)
	var se *business.StoreError
	require.ErrorAs(t, err, &se)
	require.Equal(t, business.ErrTypeValidation, se.StoreErrorType)
}
