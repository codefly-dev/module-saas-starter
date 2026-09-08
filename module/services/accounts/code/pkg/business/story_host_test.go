package business_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"accounts/pkg/adapters"
	accountsauth "accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

	// Given a signed-in person of tenant A, and someone who is not in it.
	personID, tenantA := mustUserAndOrg(t, ctx, "workcontext@example.com", "workcontext", "Work Context Co")
	outsiderID, _ := mustUserAndOrg(t, ctx, "outsider@example.com", "outsider", "Outsider Co")

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	verifier, err := codefly.NewWorkContextVerifier(codefly.WorkContextVerifierOptions{
		PublicKeys: map[string]ed25519.PublicKey{storyWorkContextKeyID: publicKey},
	})
	require.NoError(t, err)

	// The issuer under test is the host's own, resolving authority from the
	// tenant's rows. Minting with a bare SDK signer here would leave every
	// admission guard on this path — authentication, membership, the actor
	// ceiling — unexercised.
	adapters.WithService(testService)
	issuer := &adapters.WorkContextAuthorityServer{}
	issuer.Configure(adapters.WorkContextAuthorityConfiguration{
		Issuer:     storyWorkContextIssuer,
		KeyID:      storyWorkContextKeyID,
		PrivateKey: privateKey,
		Authority:  testStore,
	})

	// When they call a module through the gateway.
	issued, err := issuer.StartTask(
		storyCallerContext(ctx, personID, tenantA),
		storyTaskRequest(tenantA),
	)
	require.NoError(t, err)

	// Then the module receives a signed Work Context naming the person and tenant A.
	token, err := codefly.ParseWorkContextToken(issued.GetToken())
	require.NoError(t, err)
	claims, err := verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   storyWorkContextIssuer,
		Audience: "documents",
		TenantID: tenantA,
	})
	require.NoError(t, err)
	require.Equal(t, tenantA, claims.GetTenantId())
	require.Equal(t, personID, claims.GetOwnerPrincipalId())

	// And a Work Context minted for another audience is refused.
	_, err = verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   storyWorkContextIssuer,
		Audience: "billing",
		TenantID: tenantA,
	})
	require.Error(t, err, "a context minted for one module may not be presented to another")

	// The same holds for another tenant's expectation of the same token.
	_, err = verifier.Verify(token, codefly.WorkContextExpectations{
		Issuer:   storyWorkContextIssuer,
		Audience: "documents",
		TenantID: uuid.NewString(),
	})
	require.Error(t, err)

	// The host never mints one on a claim it has not checked: a caller who is
	// not a member of tenant A is refused, however the request is addressed.
	_, err = issuer.StartTask(
		storyCallerContext(ctx, outsiderID, tenantA),
		storyTaskRequest(tenantA),
	)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// And an unauthenticated call mints nothing.
	_, err = issuer.StartTask(ctx, storyTaskRequest(tenantA))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

const (
	storyWorkContextIssuer = "accounts.story-test"
	storyWorkContextKeyID  = "story-test-key"
)

// storyTaskRequest is one human-owned Task mint for the documents module, with
// the narrowest authority the request shape accepts.
func storyTaskRequest(orgID string) *gen.StartTaskWorkContextRequest {
	return &gen.StartTaskWorkContextRequest{
		OrgId:     orgID,
		TaskId:    uuid.NewString(),
		SessionId: uuid.NewString(),
		Audience:  "documents",
		AuthorityScopes: []*gen.WorkContextScope{{
			ResourceKind: "evidence",
			Actions:      []string{"read"},
		}},
	}
}

// storyCallerContext is what the gateway hands the issuer: the verified caller
// identity as gRPC metadata, plus the tenant-bound database identity the
// membership read is scoped to.
func storyCallerContext(ctx context.Context, userID, orgID string) context.Context {
	return metadata.NewIncomingContext(
		accountsauth.WithVerifiedDatabaseIdentity(ctx, userID, orgID),
		metadata.Pairs(string(wool.UserIDKey), userID),
	)
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
	jobPool, err := infra.NewJobWorkerPool(ctx)
	require.NoError(t, err)
	t.Cleanup(jobPool.Close)
	modules.SetModuleCapabilities(testStore, infra.NewPostgresJobStore(jobPool), business.ModulePrincipalRegistry{
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
		solution, ok := event.Payload["solution"].(string)
		require.True(t, ok, "every event on the spine carries its solution scope")
		tenantIDs[event.ID] = solution
	}
	for _, event := range solutionView {
		solution, present := tenantIDs[event.ID]
		require.True(t, present, "an event visible to a solution query must be in the tenant view")
		require.Equal(t, "alpha", solution)
	}
}
