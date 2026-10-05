package auth_test

import (
	"strings"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

func TestASolutionMCPResourceIsComposedInOnePlace(t *testing.T) {
	require.Equal(t, "https://host.example.com/api/solutions/example/proxy/mcp",
		auth.SolutionMCPResource("https://host.example.com", "example"))
	// A trailing slash on the origin must not produce a doubled separator: the
	// indicator is compared by equality downstream, so a second spelling of the
	// same resource is an audience nothing matches.
	require.Equal(t, "https://host.example.com/api/solutions/example/proxy/mcp",
		auth.SolutionMCPResource("https://host.example.com/", "example"))
}

func TestAResourceIndicatorNamesOneSolutionsMCPEndpoint(t *testing.T) {
	resource, err := auth.ParseResourceIndicator("https://host.example.com/api/solutions/example/proxy/mcp")
	require.NoError(t, err)
	require.Equal(t, "https://host.example.com/api/solutions/example/proxy/mcp", resource.Value)
	require.Equal(t, "https://host.example.com", resource.Origin)
	require.Equal(t, "example", resource.SolutionID)

	// Loopback http is accepted so local development works; nothing else is.
	_, err = auth.ParseResourceIndicator("http://localhost:3000/api/solutions/example/proxy/mcp")
	require.NoError(t, err)
}

func TestEveryOtherResourceShapeIsRefused(t *testing.T) {
	for _, candidate := range []string{
		"",
		"example",
		"/api/solutions/example/proxy/mcp",
		// Not this host's one resource shape.
		"https://host.example.com/solutions/example",
		"https://host.example.com/api/solutions/example/proxy/mcp/tools",
		"https://host.example.com/v1/users",
		"https://host.example.com/api/solutions//proxy/mcp",
		// The gateway's own internal path. It is not public — on the public
		// origin that prefix is a page of the frontend — so an audience naming
		// it is one no client could present at the resource it describes.
		"https://host.example.com/solutions/example/mcp",
		// RFC 8707 §2 forbids a fragment and discourages a query; both are
		// refused rather than normalised away, because a tolerated variant
		// would be an audience the gateway never matches.
		"https://host.example.com/api/solutions/example/proxy/mcp#x",
		"https://host.example.com/api/solutions/example/proxy/mcp?x=1",
		"https://user:pass@host.example.com/api/solutions/example/proxy/mcp",
		// Non-loopback plaintext, and a scheme that is not HTTP at all.
		"http://host.example.com/api/solutions/example/proxy/mcp",
		"ftp://host.example.com/api/solutions/example/proxy/mcp",
		// An escaped separator would read as a two-segment solution id on the
		// decoded path while the audience string carries the escaped form.
		"https://host.example.com/solutions/a%2Fb/mcp",
		// Solution ids are one lowercase segment.
		"https://host.example.com/api/solutions/Example/proxy/mcp",
		"https://host.example.com/api/solutions/-example/proxy/mcp",
		"https://host.example.com/api/solutions/example-/proxy/mcp",
	} {
		_, err := auth.ParseResourceIndicator(candidate)
		require.ErrorIs(t, err, auth.ErrResourceRejected, "should refuse %q", candidate)
	}
}

// The authorization server pins the origin to its own public base, so it cannot
// be talked into minting an audience for somebody else's host — which is the
// confused-deputy risk resource indicators exist to close.
func TestAResourceMustNameThisHostsOwnOrigin(t *testing.T) {
	resource, err := auth.RequireResourceAtOrigin(
		"https://host.example.com/api/solutions/example/proxy/mcp", "https://host.example.com")
	require.NoError(t, err)
	require.Equal(t, "example", resource.SolutionID)

	_, err = auth.RequireResourceAtOrigin(
		"https://evil.example.com/api/solutions/example/proxy/mcp", "https://host.example.com")
	require.ErrorIs(t, err, auth.ErrResourceRejected)

	// With no trusted origin there is nothing to pin against, so nothing is
	// minted. Failing closed here is what keeps a deployment that has not
	// configured its own address from issuing audiences it cannot enforce.
	_, err = auth.RequireResourceAtOrigin(
		"https://host.example.com/api/solutions/example/proxy/mcp", "")
	require.ErrorIs(t, err, auth.ErrResourceRejected)
}

func TestAValidCodeChallengeIsBase64URLInTheS256Band(t *testing.T) {
	require.True(t, auth.ValidCodeChallenge("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"))
	require.False(t, auth.ValidCodeChallenge(""))
	require.False(t, auth.ValidCodeChallenge("too-short"))
	require.False(t, auth.ValidCodeChallenge("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw+cM"))
	require.False(t, auth.ValidCodeChallenge("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw/cM"))
}

// The cross-repository contract, stated where a change to either side will trip
// over it.
//
// This host ACCEPTS exactly one resource shape, and a solution's runtime DERIVES
// the identifier it publishes. Both halves must spell the same URL or discovery
// cannot establish resource identity: RFC 9728 §3.3 has the client compare the
// `resource` in the runtime's metadata document against the URL it dialled, and
// RFC 8707 makes that identifier the audience its token is bound to — so a
// mismatch is rejected by the client, before a token is ever requested.
//
// The two live in different repositories and no compiler relates them, so this
// records the agreed string and names the other side. The runtime's derivation
// is `PUBLIC_URL + "/api/solutions/" + id + "/proxy" + "/mcp"`
// (codefly-dev/solution-runtime-go, resolveMCPPublicURL).
//
// A deployment whose host routes solutions differently overrides the runtime's
// derivation with its `mcp/public-url` configuration value; this host still
// accepts only the shape below, so such an override has to name it.
func TestTheResourceShapeThisHostAcceptsIsTheOneASolutionRuntimePublishes(t *testing.T) {
	const origin = "https://app.example.com"

	// What this host composes, which is what it requires on the way in.
	require.Equal(t,
		origin+"/api/solutions/example/proxy/mcp",
		auth.SolutionMCPResource(origin, "example"))

	// Spelled out rather than built from the same constants, so a change to
	// those constants fails here instead of agreeing with itself.
	indicator, err := auth.ParseResourceIndicator(origin + "/api/solutions/example/proxy/mcp")
	require.NoError(t, err, "the runtime's derived identifier must be one this host accepts")
	require.Equal(t, "example", indicator.SolutionID)

	// And the metadata path the challenge names is that URL's sibling, which is
	// where the runtime serves its document.
	require.Equal(t,
		"/api/solutions/example/proxy/.well-known/oauth-protected-resource",
		auth.SolutionResourceMetadataPath("example"))

	// The gateway's own internal route is NOT accepted. It is unreachable from
	// outside, so an identifier naming it could never be one a client dialled.
	_, err = auth.ParseResourceIndicator(origin + "/solutions/example/mcp")
	require.ErrorIs(t, err, auth.ErrResourceRejected)
}

// R54-04, the issuance half. The authorization server mints an audience only for
// the EXACT identifier, by code-point equality — a variant is not "the same
// resource spelled differently", it is a different string that the client it was
// issued for would reject.
//
// The pair matters: the gateway holds the same rule on admission, so neither
// side can admit a spelling the other refuses. A host that accepted several
// spellings of one resource would have several resources, and the audience
// binding would mean less than it says.
func TestOnlyTheExactResourceIdentifierIsIssuedAnAudience(t *testing.T) {
	const origin = "https://app.example.com"
	exact := auth.SolutionMCPResource(origin, "example")

	indicator, err := auth.RequireResourceAtOrigin(exact, origin)
	require.NoError(t, err)
	// Used as given (RFC 8707): the value that goes in `aud` is the request's
	// own string, never re-rendered from parsed parts.
	require.Equal(t, exact, indicator.Value)

	for _, variant := range []string{
		strings.Replace(exact, "app.example.com", "APP.example.com", 1),
		strings.Replace(exact, "app.example.com", "App.Example.Com", 1),
		strings.Replace(exact, "https://", "HTTPS://", 1),
		strings.Replace(exact, "/example/", "/Example/", 1),
		strings.Replace(exact, "app.example.com", "app.example.com:443", 1),
		exact + "/",
	} {
		_, err := auth.RequireResourceAtOrigin(variant, origin)
		require.ErrorIs(t, err, auth.ErrResourceRejected, "must refuse %q", variant)
	}
}
