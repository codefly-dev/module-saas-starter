package business_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStory_HOST_ID_001 — "Every request carries who and for whom".
//
// As a module, I want every request to arrive with a verifiable identity and
// tenant, so that I never trust a request's own claims.
//
// The authority facts come from the tenant's own rows, and the audience is
// bound into the signature, so a context minted for one module cannot be
// presented to another.
func TestStory_HOST_ID_001(t *testing.T) {
	clearData(t)
	ctx := testCtx

	// Given a signed-in person of tenant A.
	personID, tenantA := mustUserAndOrg(t, ctx, "workcontext@example.com", "workcontext", "Work Context Co")

	// The issuer resolves authority as the caller, under the caller's own
	// tenant-bound identity — the same discipline the RPC edge applies.
	callerCtx := accountsauth.WithVerifiedDatabaseIdentity(ctx, personID, tenantA)
	facts, err := testStore.ResolveWorkContextAuthority(callerCtx, tenantA, personID, "", nil)
	require.NoError(t, err)
	require.NotNil(t, facts, "the tenant's own rows are what authority is derived from")

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := codefly.NewWorkContextSigner(codefly.WorkContextSignerOptions{
		Issuer:     "accounts.story-test",
		KeyID:      "story-test-key",
		PrivateKey: privateKey,
	})
	require.NoError(t, err)
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{
		PublicKeys: map[string]ed25519.PublicKey{"story-test-key": publicKey},
	})
	require.NoError(t, err)

	// When they call a module through the gateway.
	token, _, err := signer.StartTask(codefly.StartTaskInput{
		Audience:              "documents",
		TenantID:              tenantA,
		OwnerPrincipalID:      personID,
		TaskID:                uuid.NewString(),
		SessionID:             uuid.NewString(),
		AuthorizationRevision: facts.EffectiveRevision(),
	})
	require.NoError(t, err)

	// Then the module receives a signed Work Context naming the person and tenant A.
	claims, err := verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   "accounts.story-test",
		Audience: "documents",
		TenantID: tenantA,
	})
	require.NoError(t, err)
	require.Equal(t, tenantA, claims.GetTenantId())
	require.Equal(t, personID, claims.GetOwnerPrincipalId())

	// And a Work Context minted for another audience is refused.
	_, err = verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   "accounts.story-test",
		Audience: "billing",
		TenantID: tenantA,
	})
	require.Error(t, err, "a context minted for one module may not be presented to another")

	// The same holds for another tenant's expectation of the same token.
	_, err = verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   "accounts.story-test",
		Audience: "documents",
		TenantID: uuid.NewString(),
	})
	require.Error(t, err)
}

// TestStory_HOST_AUD_001 — "One audit spine, two views".
//
// As a compliance officer, I want to see everything my organisation did, and
// everything one solution did, from one place, so that audit is complete and
// comparable.
//
// Both views are queries over the same spine: the tenant view is the whole
// organisation's events, the solution view narrows the same rows by the scope
// the host stamped on them, so the two can never disagree.
func TestStory_HOST_AUD_001(t *testing.T) {
	clearData(t)
	ctx := testCtx

	actorID, tenantA := mustUserAndOrg(t, ctx, "compliance@example.com", "compliance", "Compliance Co")

	modulePrincipal := uuid.NewString()
	modules, err := business.NewService(testStore)
	require.NoError(t, err)
	emitter, err := business.NewDurableAuditEmitter(testStore, testStore)
	require.NoError(t, err)
	t.Cleanup(emitter.Close)
	modules.SetAuditEmitter(emitter)
	modules.SetModuleCapabilities(testStore, nil, business.ModulePrincipalRegistry{
		modulePrincipal: {Queues: []string{"documents"}},
	})
	caller := business.ModuleCaller{PrincipalID: modulePrincipal, BoundOrg: tenantA}

	// Given events emitted by several modules and solutions for tenant A.
	emitted := []struct {
		solution string
		entry    string
	}{
		{solution: "alpha", entry: "entry-alpha-1"},
		{solution: "alpha", entry: "entry-alpha-2"},
		{solution: "beta", entry: "entry-beta-1"},
	}
	for _, event := range emitted {
		require.NoError(t, modules.ModuleEmitAuditEvent(
			ctx, caller, tenantA, "document.ingested", actorID, event.solution, event.entry, nil,
		))
	}

	// When I query the audit log by tenant.
	tenantView, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID:     tenantA,
		EventType: "document.ingested",
		PageSize:  50,
	})
	require.NoError(t, err)
	require.Len(t, tenantView, len(emitted), "the tenant view is every solution's events")

	// Or by solution scope.
	solutionView, _, _, err := testService.QueryAuditLog(ctx, business.AuditQuery{
		OrgID:           tenantA,
		EventType:       "document.ingested",
		PayloadContains: map[string]any{"solution": "alpha"},
		PageSize:        50,
	})
	require.NoError(t, err)
	require.Len(t, solutionView, 2)

	// Then both views are complete and consistent: the narrower view is a
	// subset of the wider one, event for event.
	tenantIDs := make(map[string]string, len(tenantView))
	for _, event := range tenantView {
		tenantIDs[event.ID] = event.Payload["solution"].(string)
	}
	for _, event := range solutionView {
		solution, present := tenantIDs[event.ID]
		require.True(t, present, "an event visible to a solution query must be in the tenant view")
		require.Equal(t, "alpha", solution)
	}
}
