package main

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"

	carrier "github.com/codefly-dev/sdk-go/workcontext"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// signingIssuer is the authority's Issue for a person: it signs a real Work
// Context addressed to the audience asked for, under the key this edge
// verifies with, or answers the refusal it is given.
type signingIssuer struct {
	t      *testing.T
	kid    string
	key    ed25519.PrivateKey
	refuse error

	mu       sync.Mutex
	calls    int
	bearer   string
	audience string
	person   bool
}

func (f *signingIssuer) Issue(ctx context.Context, in *authorityv1.IssueRequest, _ ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	f.bearer = strings.Join(md.Get("authorization"), ",")
	f.audience, f.person = in.GetAudience(), in.GetPerson() != nil
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &authorityv1.IssueResponse{Token: mintWorkContextAudience(f.t, f.kid, f.key, in.GetAudience()), ExpiresAtUnix: time.Now().Add(5 * time.Minute).Unix()}, nil
}

// solutionPrincipal gives a declared solution the authority principal its
// applied manifest would carry.
func solutionPrincipal(t *testing.T, gw *Gateway, alias, principal string) {
	t.Helper()
	gw.solutions.mu.Lock()
	defer gw.solutions.mu.Unlock()
	if gw.solutions.principals == nil {
		gw.solutions.principals = map[string]string{}
	}
	gw.solutions.principals[alias] = principal
}

// audiencedHarness is a gateway that verifies Work Contexts and mints a
// person's at a solution, with two solutions that each declare a principal.
func audiencedHarness(t *testing.T) (gw *Gateway, api, audit, other *fakeUpstream, issuer *signingIssuer, session string) {
	t.Helper()
	gw, api, accessPriv, wcPriv, kid := edgeHarness(t)
	audit = registerSolutionUpstream(t, gw, "audit")
	other = registerSolutionUpstream(t, gw, "other")
	solutionPrincipal(t, gw, "audit", "audit-principal")
	solutionPrincipal(t, gw, "other", "other-principal")
	issuer = &signingIssuer{t: t, kid: kid, key: wcPriv}
	gw.personContexts = &hopRole{issuer: issuer}
	return gw, api, audit, other, issuer, signValidToken(t, accessPriv)
}

// What a solution is handed cannot be replayed. The gateway used to forward
// the person's own bearer, a credential for the whole host: a solution holding
// it could call the host's API and every other solution as that person. A
// solution now receives the person's Work Context addressed to it alone, and
// nothing of their session. The proof is the replay itself: every header the
// solution received, presented back at the host's API and at another
// solution, is refused at both, and so is the credential presented as a
// bearer.
func TestWhatASolutionReceivesIsRefusedAtTheHostAPIAndAtAnotherSolution(t *testing.T) {
	gw, api, audit, other, issuer, session := audiencedHarness(t)

	request := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	request.Header.Set("Authorization", "Bearer "+session)
	request.Header.Set("Cookie", "codefly_rt=a-refresh-token")
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	received := audit.lastHeaders.Clone()
	require.NotNil(t, received, "the solution was reached")

	replay := func(path string, headers http.Header) int {
		t.Helper()
		again := httptest.NewRequest(http.MethodGet, path, nil)
		again.Header = headers.Clone()
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, again)
		return w.Code
	}
	api.lastHeaders, other.lastHeaders = nil, nil
	require.Equal(t, http.StatusUnauthorized, replay("/v1/users", received), "what the solution received, replayed at the host's API")
	require.Nil(t, api.lastHeaders, "the replay never reached accounts")
	require.Equal(t, http.StatusUnauthorized, replay("/solutions/other/v1/data", received), "what the solution received, replayed at another solution")
	require.Nil(t, other.lastHeaders, "the replay never reached the other solution")

	// And whatever credential it holds, presented as a bearer, is no session.
	for name, values := range received {
		for _, value := range values {
			if !strings.Contains(value, ".") || len(value) < 64 {
				continue // not a token
			}
			asBearer := http.Header{"Authorization": {"Bearer " + strings.TrimPrefix(value, "Bearer ")}}
			require.Equal(t, http.StatusUnauthorized, replay("/v1/users", asBearer), "%s as a bearer at the host's API", name)
			require.Equal(t, http.StatusUnauthorized, replay("/solutions/other/v1/data", asBearer), "%s as a bearer at another solution", name)
		}
	}
	require.Nil(t, api.lastHeaders)
	require.Nil(t, other.lastHeaders)

	// What it did receive: the person's Work Context at this solution, issued
	// by the authority for their own session, and who they are.
	require.Empty(t, received.Get("Authorization"), "the person's session does not travel")
	require.Empty(t, received.Get("Cookie"))
	require.NotEmpty(t, received.Get(carrier.HeaderName), "the solution is handed a credential addressed to it")
	inspected, err := gw.workContext.Inspect(context.Background(), received.Get(carrier.HeaderName), "audit-principal")
	require.NoError(t, err, "addressed to this solution's own principal")
	require.Equal(t, "audit-principal", inspected.Audience())
	require.NotEmpty(t, received.Get("x-user-id"), "the solution still learns who the person is")
	require.Equal(t, "audit-principal", issuer.audience)
	require.True(t, issuer.person, "the Work Context is the person's")
	require.Equal(t, "Bearer "+session, issuer.bearer, "the authority authenticates the person by their own session")

	// The same person at the same solution is not minted for again while it holds.
	gw.ServeHTTP(httptest.NewRecorder(), func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
		r.Header.Set("Authorization", "Bearer "+session)
		return r
	}())
	require.Equal(t, 1, issuer.calls)
}

// The authority decides whether the person holds anything at the solution. Its
// refusal is the request's, by its reason, and nothing is forwarded: never the
// session in place of the credential that was refused.
func TestASolutionTheAuthorityRefusesThePersonAtReceivesNothing(t *testing.T) {
	gw, _, audit, _, issuer, session := audiencedHarness(t)
	for _, tc := range []struct {
		name   string
		refuse error
		want   int
	}{
		{"the person holds no role there", status.Error(codes.PermissionDenied, "jane holds no role on audit"), http.StatusForbidden},
		{"an API key or an ended session is no person's session", status.Error(codes.Unauthenticated, "the bearer names no open session"), http.StatusUnauthorized},
		{"the authority gave no answer", status.Error(codes.Unavailable, "dial tcp 10.0.0.1:443: connection refused"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer.refuse, audit.lastHeaders = tc.refuse, nil
			request := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
			request.Header.Set("Authorization", "Bearer "+session)
			recorder := httptest.NewRecorder()
			gw.ServeHTTP(recorder, request)
			require.Equal(t, tc.want, recorder.Code, recorder.Body.String())
			require.Nil(t, audit.lastHeaders, "nothing reached the solution")
			require.NotContains(t, recorder.Body.String(), "10.0.0.1", "a failure's transport detail is not the caller's to read")
		})
	}

	// A gateway that can mint nothing hands a solution that declares a
	// principal nothing either.
	gw.personContexts, audit.lastHeaders = nil, nil
	request := httptest.NewRequest(http.MethodGet, "/solutions/audit/v1/audit/logs", nil)
	request.Header.Set("Authorization", "Bearer "+session)
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Nil(t, audit.lastHeaders)
}

// A solution whose delivery declares no authority principal has no audience a
// credential could name: it is handed who the person is and no credential at
// all, never their session.
func TestASolutionDeclaringNoPrincipalReceivesNoCredential(t *testing.T) {
	gw, _, _, priv := newGatewayHarness(t)
	plain := registerSolutionUpstream(t, gw, "plain")
	request := httptest.NewRequest(http.MethodGet, "/solutions/plain/v1/data", nil)
	request.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	recorder := httptest.NewRecorder()
	gw.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, plain.lastHeaders.Get("Authorization"), "the person's session does not travel")
	require.Empty(t, plain.lastHeaders.Get(carrier.HeaderName))
	require.NotEmpty(t, plain.lastHeaders.Get("x-user-id"))
}
