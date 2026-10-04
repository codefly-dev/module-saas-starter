package auth_test

import (
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
