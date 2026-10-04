package auth_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

// claudeCodeDocument is the document Claude Code actually publishes at
// https://claude.ai/oauth/claude-code-client-metadata, captured verbatim on
// 2026-10-04. The point of testing against the real shape rather than an
// invented one is that this host is useless if it refuses the client it exists
// to admit, and every field below is one a hand-written fixture could get
// wrong: there is no `scope`, the redirect URIs carry NO port, and
// `token_endpoint_auth_method` is the only auth-method member present.
const claudeCodeClientID = "https://claude.ai/oauth/claude-code-client-metadata"

const claudeCodeDocument = `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","client_name":"Claude Code","client_uri":"https://claude.ai","redirect_uris":["http://localhost/callback","http://127.0.0.1/callback"],"grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none"}`

func TestClaudeCodeMetadataDocumentIsAdmittedAsPublished(t *testing.T) {
	client, err := auth.ClientFromMetadataDocument(claudeCodeClientID, []byte(claudeCodeDocument))
	require.NoError(t, err)
	require.True(t, client.Metadata)
	require.Equal(t, claudeCodeClientID, client.ClientID)
	require.Equal(t, "Claude Code", client.Name)
	require.Equal(t, "https://claude.ai", client.URI)
	require.Equal(t,
		[]string{"http://localhost/callback", "http://127.0.0.1/callback"},
		client.RedirectURIs)
	// A metadata client never gets a browser origin. The gateway binds a token
	// to the origins a client registered, and a client that published its own
	// would be choosing its own CORS grant.
	require.Empty(t, client.Origins)
}

// The loopback port rule (RFC 8252 §7.3) is what makes Claude Code's document
// usable at all: it listens on an ephemeral port it cannot know in advance, and
// the document it published names no port.
func TestLoopbackRedirectWithoutAPortMatchesAnyPort(t *testing.T) {
	client, err := auth.ClientFromMetadataDocument(claudeCodeClientID, []byte(claudeCodeDocument))
	require.NoError(t, err)

	for _, presented := range []string{
		"http://localhost/callback",
		"http://localhost:54321/callback",
		"http://localhost:1/callback",
		"http://127.0.0.1:8976/callback",
	} {
		require.True(t, client.AllowsRedirect(presented), "should admit %q", presented)
	}
	for _, presented := range []string{
		// A different path is a different redirect, port or no port.
		"http://localhost:54321/other",
		"http://localhost:54321/callback/sub",
		// https is not the registered scheme.
		"https://localhost:54321/callback",
		// A different loopback spelling is a different host: ::1 is not 127.0.0.1,
		// and admitting one for the other would widen a declaration nobody made.
		"http://[::1]:54321/callback",
		// Not loopback at all.
		"http://evil.example.com:54321/callback",
		// A query the registration did not declare.
		"http://localhost:54321/callback?next=https://evil.example.com",
		// A fragment is invisible to the server issuing the redirect.
		"http://localhost:54321/callback#x",
	} {
		require.False(t, client.AllowsRedirect(presented), "should refuse %q", presented)
	}
}

// The rule must not widen a registration that names a port. Every client the
// operator declared today names one, so enabling metadata documents cannot
// change what an existing registration means.
func TestARegisteredLoopbackPortStaysExact(t *testing.T) {
	registry, err := auth.NewClientRegistry(`[{
		"client_id": "example-cli",
		"name": "Example CLI",
		"redirect_uris": ["http://localhost:3000/auth/callback"]
	}]`)
	require.NoError(t, err)
	client, ok := registry.Lookup("example-cli")
	require.True(t, ok)

	require.True(t, client.AllowsRedirect("http://localhost:3000/auth/callback"))
	require.False(t, client.AllowsRedirect("http://localhost:3001/auth/callback"))
	require.False(t, client.AllowsRedirect("http://localhost/auth/callback"))
}

// Each validation rule, refused by name. The public answer collapses to one
// sentinel so an unauthenticated caller cannot enumerate the registry, but the
// reason must survive in the error an operator reads — and in these tests,
// which are the only thing that holds each rule separately.
func TestEachMetadataDocumentRuleIsRefusedByName(t *testing.T) {
	tests := []struct {
		name     string
		clientID string
		document string
		wantErr  error
	}{
		{
			name:     "a document naming a different client_id",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://evil.example.com/metadata","redirect_uris":["https://evil.example.com/cb"],"token_endpoint_auth_method":"none"}`,
			wantErr:  auth.ErrClientMetadataIdentityMismatch,
		},
		{
			name:     "a document declaring no auth method",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":["http://localhost/callback"]}`,
			wantErr:  auth.ErrClientMetadataAuthMethod,
		},
		{
			name:     "a document declaring a confidential auth method",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":["http://localhost/callback"],"token_endpoint_auth_method":"client_secret_basic"}`,
			wantErr:  auth.ErrClientMetadataAuthMethod,
		},
		{
			name:     "a document with no redirect URI",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":[],"token_endpoint_auth_method":"none"}`,
			wantErr:  auth.ErrClientMetadataRedirectURIs,
		},
		{
			name:     "a document whose only redirect URI is non-loopback http",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":["http://evil.example.com/cb"],"token_endpoint_auth_method":"none"}`,
			wantErr:  auth.ErrClientMetadataRedirectURIs,
		},
		{
			name:     "a document not declaring the authorization_code grant",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":["http://localhost/callback"],"grant_types":["client_credentials"],"token_endpoint_auth_method":"none"}`,
			wantErr:  auth.ErrClientMetadataGrantTypes,
		},
		{
			name:     "a document not declaring the code response type",
			clientID: claudeCodeClientID,
			document: `{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","redirect_uris":["http://localhost/callback"],"response_types":["token"],"token_endpoint_auth_method":"none"}`,
			wantErr:  auth.ErrClientMetadataGrantTypes,
		},
		{
			name:     "a document that is not JSON",
			clientID: claudeCodeClientID,
			document: `not json`,
			wantErr:  auth.ErrClientMetadataMalformed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := auth.ClientFromMetadataDocument(test.clientID, []byte(test.document))
			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

// A document whose name is absent or absurd falls back to the one fact about
// the client this host verified: the origin it served the document from.
func TestAMetadataClientWithNoUsableNameIsNamedByItsOrigin(t *testing.T) {
	client, err := auth.ClientFromMetadataDocument(claudeCodeClientID,
		[]byte(`{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","client_name":"   ","redirect_uris":["http://localhost/callback"],"token_endpoint_auth_method":"none"}`))
	require.NoError(t, err)
	require.Equal(t, "https://claude.ai", client.Name)

	long := strings.Repeat("a", 200)
	client, err = auth.ClientFromMetadataDocument(claudeCodeClientID,
		[]byte(`{"client_id":"https://claude.ai/oauth/claude-code-client-metadata","client_name":"`+long+`","redirect_uris":["http://localhost/callback"],"token_endpoint_auth_method":"none"}`))
	require.NoError(t, err)
	require.Equal(t, "https://claude.ai", client.Name)
}

func TestAClientIDMustBeAnHTTPSURLWithAPath(t *testing.T) {
	for _, candidate := range []string{
		"",
		"example-addin",
		"http://claude.ai/oauth/metadata",
		"https://claude.ai",
		"https://claude.ai/",
		"https://user:pass@claude.ai/metadata",
		"https://claude.ai/metadata#frag",
		"https://claude.ai/metadata?q=1",
	} {
		_, err := auth.CanonicalMetadataClientID(candidate)
		require.ErrorIs(t, err, auth.ErrClientMetadataClientID, "should refuse %q", candidate)
	}
	canonical, err := auth.CanonicalMetadataClientID("  " + claudeCodeClientID + "  ")
	require.NoError(t, err)
	require.Equal(t, claudeCodeClientID, canonical)
}

// The two namespaces cannot collide, which is what makes the fallback to a
// metadata document unambiguous: a registry slug can never spell `https://`.
func TestARegistrySlugIsNeverReadAsAMetadataClientID(t *testing.T) {
	require.False(t, auth.IsMetadataClientID("example-addin"))
	require.True(t, auth.IsMetadataClientID(claudeCodeClientID))
	require.False(t, auth.ValidClientID(claudeCodeClientID))
}

func TestAnUnconfiguredPolicyAdmitsNoMetadataClient(t *testing.T) {
	policy, err := auth.NewClientMetadataPolicy("")
	require.NoError(t, err)
	require.False(t, policy.Enabled())
	require.ErrorIs(t, policy.Admits(claudeCodeClientID), auth.ErrClientMetadataDisabled)

	resolver := auth.NewClientMetadataResolver(policy)
	require.False(t, resolver.Enabled())
	_, err = resolver.Resolve(context.Background(), claudeCodeClientID)
	require.ErrorIs(t, err, auth.ErrClientMetadataDisabled)
}

func TestAnAllowlistPolicyAdmitsOnlyWhatItNames(t *testing.T) {
	policy, err := auth.NewClientMetadataPolicy(claudeCodeClientID)
	require.NoError(t, err)
	require.True(t, policy.Enabled())
	require.NoError(t, policy.Admits(claudeCodeClientID))
	require.ErrorIs(t, policy.Admits("https://other.example.com/metadata"),
		auth.ErrClientMetadataNotAllowed)

	anyPolicy, err := auth.NewClientMetadataPolicy("any")
	require.NoError(t, err)
	require.NoError(t, anyPolicy.Admits("https://other.example.com/metadata"))
}

// A malformed declaration fails startup. An allowlist that silently drops the
// entry it could not read either locks out a working client or admits more than
// was written.
func TestAMalformedMetadataPolicyFailsStartup(t *testing.T) {
	for _, declaration := range []string{
		"not-a-url",
		"http://claude.ai/metadata",
		",",
	} {
		_, err := auth.NewClientMetadataPolicy(declaration)
		require.Error(t, err, "should refuse %q", declaration)
	}
}

// The fetch half, against a real TLS server. Both test hooks are needed: the
// SSRF dial guard refuses every loopback address, and the transport pins TLS to
// the public roots, which no test certificate is in.
func TestTheResolverFetchesValidatesAndCachesADocument(t *testing.T) {
	allowLoopbackMetadataDial(t)

	var fetches int
	var clientID string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"` + clientID + `","client_name":"Example MCP Client","redirect_uris":["http://localhost/callback"],"token_endpoint_auth_method":"none"}`))
	}))
	defer server.Close()
	clientID = server.URL + "/metadata"

	resolver := testResolver(t, "any", server)
	client, err := resolver.Resolve(context.Background(), clientID)
	require.NoError(t, err)
	require.Equal(t, "Example MCP Client", client.Name)
	require.True(t, client.Metadata)
	require.Equal(t, 1, fetches)

	// Cached for the document's own freshness, so an authorize request does not
	// cost an outbound fetch every time.
	_, err = resolver.Resolve(context.Background(), clientID)
	require.NoError(t, err)
	require.Equal(t, 1, fetches)
}

// A refusal is cached too, briefly. Without that, an unauthenticated caller can
// make this host fetch an arbitrary https URL once per request.
func TestARefusedDocumentIsCachedBriefly(t *testing.T) {
	allowLoopbackMetadataDial(t)

	var fetches int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	clientID := server.URL + "/metadata"

	resolver := testResolver(t, "any", server)
	for range 3 {
		_, err := resolver.Resolve(context.Background(), clientID)
		require.ErrorIs(t, err, auth.ErrClientMetadataUnreachable)
	}
	require.Equal(t, 1, fetches)

	// Past the negative TTL it is tried again: a client that fixed its document
	// must not be locked out for longer than that.
	now := time.Now().Add(2 * time.Minute)
	resolver.SetClockForTest(func() time.Time { return now })
	_, err := resolver.Resolve(context.Background(), clientID)
	require.Error(t, err)
	require.Equal(t, 2, fetches)
}

// A redirect off the document's own origin is refused. The origin IS the
// client's identity, so following one would let any site that can publish a
// redirect claim another's client_id.
func TestARedirectOffTheOriginIsRefused(t *testing.T) {
	allowLoopbackMetadataDial(t)

	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, elsewhere.URL+"/metadata", http.StatusFound)
	}))
	defer server.Close()

	resolver := testResolver(t, "any", server)
	_, err := resolver.Resolve(context.Background(), server.URL+"/metadata")
	require.ErrorIs(t, err, auth.ErrClientMetadataRedirected)
}

// A body above the bound is refused rather than truncated.
func TestADocumentOverTheSizeBoundIsRefused(t *testing.T) {
	allowLoopbackMetadataDial(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", 70<<10) + `"}`))
	}))
	defer server.Close()

	resolver := testResolver(t, "any", server)
	_, err := resolver.Resolve(context.Background(), server.URL+"/metadata")
	require.ErrorIs(t, err, auth.ErrClientMetadataTooLarge)
}

// A document outside the allowlist is never fetched at all. An operator who
// named three client_id URLs has not agreed to this host making requests to a
// fourth.
func TestADisallowedClientIDIsNeverFetched(t *testing.T) {
	allowLoopbackMetadataDial(t)

	var fetches int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	resolver := testResolver(t, "https://claude.ai/oauth/claude-code-client-metadata", server)
	_, err := resolver.Resolve(context.Background(), server.URL+"/metadata")
	require.ErrorIs(t, err, auth.ErrClientMetadataNotAllowed)
	require.Equal(t, 0, fetches)
}

// A document's own Cache-Control is honoured within bounds. The floor stops a
// no-store document turning every authorize request into an outbound fetch; the
// ceiling stops a client's rotation of its redirect URIs taking a day to apply.
func TestDocumentFreshnessIsBounded(t *testing.T) {
	require.Equal(t, 5*time.Minute, auth.CachedMetadataTTLForTest("public, max-age=300"))
	require.Equal(t, time.Minute, auth.CachedMetadataTTLForTest("no-store"))
	require.Equal(t, time.Minute, auth.CachedMetadataTTLForTest(""))
	require.Equal(t, time.Minute, auth.CachedMetadataTTLForTest("max-age=1"))
	require.Equal(t, time.Hour, auth.CachedMetadataTTLForTest("max-age=86400"))
}

func testResolver(t *testing.T, declaration string, server *httptest.Server) *auth.ClientMetadataResolver {
	t.Helper()
	policy, err := auth.NewClientMetadataPolicy(declaration)
	require.NoError(t, err)
	resolver := auth.NewClientMetadataResolver(policy)
	// The test server's certificate is not in the public roots, which the
	// production transport is pinned to. Only the TLS trust is relaxed; the size,
	// time and redirect bounds are the production ones.
	resolver.SetTransportForTest(server.Client().Transport)
	return resolver
}

// allowLoopbackMetadataDial is the one seam the SSRF guard has. Production
// refuses every non-public address at dial time, after DNS resolution, which is
// what closes the rebinding gap a hostname check leaves open.
func allowLoopbackMetadataDial(t *testing.T) {
	t.Helper()
	restore := auth.SetMetadataDialGuardForTest(func(_, _ string, _ syscall.RawConn) error { return nil })
	t.Cleanup(restore)
}

// The guard itself: the addresses it refuses are the ones an attacker-supplied
// client_id would be aimed at.
func TestTheMetadataDialGuardRefusesEveryNonPublicAddress(t *testing.T) {
	guard := auth.MetadataDialGuardForTest()
	for _, address := range []string{
		"127.0.0.1:443",
		"[::1]:443",
		"10.0.0.1:443",
		"192.168.1.1:443",
		"172.16.0.1:443",
		"169.254.169.254:443",
		"not-an-address",
	} {
		require.Error(t, guard("tcp", address, nil), "should refuse %q", address)
	}
	require.NoError(t, guard("tcp", net.JoinHostPort("93.184.216.34", "443"), nil))
}
