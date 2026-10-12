package main

// THE MODULE READ A RESOURCE-CONFINED CREDENTIAL IS DELIVERED FOR, end to end
// through this host's real Gateway.ServeHTTP, on core/workcontext throughout.
//
// The flow is three requests:
//
//  1. the client calls the solution's tool endpoint with a credential confined to
//     that solution;
//  2. the solution's runtime mints a Work Context for the composed module it
//     declared it consumes, forwarding the same credential (StartTask);
//  3. it reads `/v1/<alias>/*` with that capability and the same credential.
//
// What makes it end to end rather than three unit tests: the capability that
// reaches step 3 is the one step 2 returned, signed by core's Authority in the
// session the gateway projected on step 2; the edge holds it to core's
// Inspector; and the module answers only once core's full Verifier — the check
// a consuming module runs — accepts it. A capability in any other format, or in
// any other session, fails here.
//
// accounts is a double on step 2: this module cannot import accounts. Its half —
// that StartTask signs exactly this shape, in the caller's verified session,
// sealed to the audience's installation and verifiable by core — is pinned in
// accounts' own suite (TestStartTaskMintsWhatACoreVerifierReadsForAModule), and
// the two meet, this gateway in front of the real StartTask, in
// qualification/module-authority
// (TestTheModuleReadThroughTheRealGatewayIsMintedByTheRealStartTask).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/core/workcontext"
	carrier "github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
)

// viewerSeals is the issuer's live state for one person and one installation,
// as accounts answers it: the person holds the installation, bears no execution,
// and the organization is at one authorization revision.
func viewerSeals(t *testing.T, owner, installation string) *workcontext.MemorySealSource {
	t.Helper()
	seals := workcontext.NewMemorySealSource()
	require.NoError(t, seals.Put(owner, workcontext.Seal{InstallationID: installation, InstallationRevision: 1}))
	require.NoError(t, seals.PutEpoch(owner, 1))
	require.NoError(t, seals.PutBearsNoExecution(owner))
	return seals
}

// moduleReading is a composed module's upstream: it verifies the presented
// capability with core's full Verifier for its own audience and answers with a
// document only when the capability is current.
type moduleReading struct {
	t        *testing.T
	verifier *workcontext.Verifier
	verified *basev0.WorkContextV1
}

func (m *moduleReading) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	verified, err := m.verifier.Verify(r.Context(), r.Header.Get(carrier.HeaderName))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	m.verified = verified.Context()
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"collection": "the viewer's documents"})
}

// declareReadingModule is declareModule with a module that verifies.
func declareReadingModule(t *testing.T, gw *Gateway, alias string, module http.Handler) {
	t.Helper()
	srv := httptest.NewServer(module)
	t.Cleanup(srv.Close)
	registry := solutionRegistryFake(t, gw)
	registry.mu.Lock()
	registry.revision++
	registry.records[alias] = &accountsv1.SolutionRegistration{
		SolutionId: alias,
		Publisher:  "solution:" + alias,
		Revision:   registry.revision,
		Declared: &accountsv1.SolutionDeclaredBinding{
			BindingId:  "acme.test." + alias,
			Generation: 1,
			Release:    "acme/" + alias + "@1.0.0",
			TargetId:   fakeSolutionTarget(alias),
			Kind:       accountsv1.SolutionDeclaredKind_SOLUTION_DECLARED_KIND_MODULE,
		},
		Backend: &accountsv1.SolutionBackendBinding{
			Revision: registry.revision, Upstream: srv.URL,
			ServiceAlias: alias, ContractVersion: "v1",
		},
	}
	registry.mu.Unlock()
	require.NoError(t, gw.solutions.refresh(context.Background()))
}

// mcpModuleReadHarness is confinedModuleHarness with a module that verifies
// with core, and the issuer's state that module reads.
func mcpModuleReadHarness(t *testing.T) (
	gw *Gateway, solution, accounts *fakeUpstream, module *moduleReading,
	caller confinedCaller, authority *workcontext.Authority,
) {
	t.Helper()
	withPublicBase(t, testPublicBase)
	gw, accounts, _, access := newGatewayHarness(t)
	pub, wcPriv := mustEd25519(t)
	const kid = "key-1"
	jwks := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	gw.workContext = newWorkContextVerifier(jwks.URL)
	solution = registerSolutionUpstream(t, gw, "example")
	gw.solutionEntitlements = &entitledTo{ids: []string{
		fakeSolutionTarget("example"), fakeSolutionTarget("documents"),
	}}
	caller = signConfinedCaller(t, access, solutionMCPResource(testPublicBase, "example"))

	installation := "install-" + fakeSolutionTarget("documents")
	seals := viewerSeals(t, caller.subject, installation)
	authority = &workcontext.Authority{
		Issuer: workContextIssuer, KeyID: kid, Key: wcPriv,
		Revisions: workcontext.FixedRevision(1), Seals: seals,
	}
	module = &moduleReading{t: t, verifier: &workcontext.Verifier{
		Issuer:    workContextIssuer,
		Audience:  "documents",
		Keys:      map[string]ed25519.PublicKey{kid: pub},
		Revisions: workcontext.FixedRevision(1),
		Replay:    workcontext.NewMemoryReplayStore(),
		Grants:    noGrants{},
		Seals:     seals,
	}}
	declareReadingModule(t, gw, "documents", module)
	return gw, solution, accounts, module, caller, authority
}

type noGrants struct{}

func (noGrants) Grant(context.Context, string) (*workcontext.Grant, error) {
	return nil, context.Canceled
}

// issueAsAccounts is what accounts' StartTask signs for the projection the
// gateway forwarded: owned by the viewer, in their organization, addressed to
// the module, sealed to the module's installation, in the session the gateway
// projected — the caller's verified session, which is the one accounts binds.
func issueAsAccounts(t *testing.T, authority *workcontext.Authority, projected http.Header, installation string) string {
	t.Helper()
	token, _, err := authority.Start(context.Background(), workcontext.StartInput{
		TenantID:           projected.Get("X-Org-Id"),
		OwnerPrincipalID:   projected.Get("X-User-Id"),
		OwnerPrincipalKind: "human",
		OrganizationID:     projected.Get("X-Org-Id"),
		TaskID:             "task-of-the-runtime",
		Audience:           "documents",
		AuthorityScopes:    []*basev0.WorkScopeV1{{ResourceKind: "documents", Actions: []string{"read"}}},
		InstallationID:     installation,
		SessionID:          projected.Get("X-Session-Id"),
		TTL:                5 * time.Minute,
	})
	require.NoError(t, err)
	return token
}

func TestMCPModuleReadThroughTheRealGatewayOnCoreWorkContexts(t *testing.T) {
	gw, solution, accounts, module, caller, authority := mcpModuleReadHarness(t)

	// 1. The tool call reaches the solution it was delivered for.
	tool := httptest.NewRequest(http.MethodPost, "/solutions/example/mcp", nil)
	tool.Header.Set("authorization", "Bearer "+caller.bearer)
	call := httptest.NewRecorder()
	gw.ServeHTTP(call, tool)
	require.Equal(t, http.StatusOK, call.Code, call.Body.String())
	require.NotNil(t, solution.lastHeaders, "the tool call reaches the solution's runtime")

	// 2. The runtime mints a capability for the module, with the same credential.
	mint := httptest.NewRecorder()
	gw.ServeHTTP(mint, mintRequestOf(caller, nil))
	require.Equal(t, http.StatusOK, mint.Code, mint.Body.String())
	require.Equal(t, caller.session, accounts.lastHeaders.Get("X-Session-Id"),
		"the mint is told the credential's own session, which is the one accounts binds")
	capability := issueAsAccounts(t, authority, accounts.lastHeaders, "install-"+fakeSolutionTarget("documents"))

	// The runtime's own read of what it was issued: core's structural decode,
	// which refuses any other encoding before it reads a field.
	decoded, err := workcontext.Decode(capability)
	require.NoError(t, err)
	require.Equal(t, "documents", decoded.Context().GetAudience())

	// 3. The module read, with the capability and the same credential.
	read := httptest.NewRecorder()
	gw.ServeHTTP(read, moduleRequest(caller, capability))
	require.Equal(t, http.StatusOK, read.Code, read.Body.String())
	require.JSONEq(t, `{"collection":"the viewer's documents"}`, read.Body.String(),
		"the module answered, which it does only once core's full Verifier accepts the capability")
	require.NotNil(t, module.verified)
	require.Equal(t, caller.subject, module.verified.GetOwnerPrincipalId())
	require.Equal(t, caller.session, module.verified.GetSessionId())
}

// The same flow with a capability the issuer has since revoked: the edge
// forwards it — a hop consults no live state — and the module, verifying at
// full strength, refuses it. One consumer per capability: the hop inspects, the
// callee verifies.
func TestMCPModuleReadARevokedCapabilityIsRefusedByTheModuleNotTheEdge(t *testing.T) {
	gw, _, accounts, module, caller, authority := mcpModuleReadHarness(t)

	mint := httptest.NewRecorder()
	gw.ServeHTTP(mint, mintRequestOf(caller, nil))
	require.Equal(t, http.StatusOK, mint.Code)
	capability := issueAsAccounts(t, authority, accounts.lastHeaders, "install-"+fakeSolutionTarget("documents"))

	// The person's authority moves on: their epoch advances.
	require.NoError(t, module.verifier.Seals.(*workcontext.MemorySealSource).PutEpoch(caller.subject, 2))

	read := httptest.NewRecorder()
	gw.ServeHTTP(read, moduleRequest(caller, capability))
	require.Equal(t, http.StatusUnauthorized, read.Code)
	require.Contains(t, read.Body.String(), "epoch")
	require.Nil(t, module.verified)
}

// A capability minted in ANY other session — the same person's unconfined
// browser session included — is refused at the edge before the module is
// reached: the pairing is on the session the confined credential names.
func TestMCPModuleReadACapabilityFromAnotherSessionIsRefusedAtTheEdge(t *testing.T) {
	gw, _, accounts, module, caller, authority := mcpModuleReadHarness(t)

	mint := httptest.NewRecorder()
	gw.ServeHTTP(mint, mintRequestOf(caller, nil))
	require.Equal(t, http.StatusOK, mint.Code)
	projected := accounts.lastHeaders.Clone()
	projected.Set("X-Session-Id", "the-viewers-browser-session")
	capability := issueAsAccounts(t, authority, projected, "install-"+fakeSolutionTarget("documents"))

	read := httptest.NewRecorder()
	gw.ServeHTTP(read, moduleRequest(caller, capability))
	require.Equal(t, http.StatusUnauthorized, read.Code)
	require.Nil(t, module.verified)
}
