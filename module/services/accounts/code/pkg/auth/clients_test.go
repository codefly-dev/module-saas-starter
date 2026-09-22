package auth_test

import (
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

const oneClient = `[{
	"client_id": "example-addin",
	"name": "Example Add-in",
	"kind": "public",
	"redirect_uris": ["https://localhost:3000/auth/callback", "https://addin.example.com/auth/callback"],
	"origins": ["https://addin.example.com"]
}]`

func TestClientRegistryResolvesADeclaredClient(t *testing.T) {
	registry, err := auth.NewClientRegistry(oneClient)
	require.NoError(t, err)

	client, ok := registry.Lookup("example-addin")
	require.True(t, ok)
	require.Equal(t, "Example Add-in", client.Name)
	require.Equal(t, []string{"https://addin.example.com"}, client.Origins)
	require.Len(t, registry.All(), 1)
}

// An unconfigured registry is the deployment that declared no client. It must
// refuse every lookup rather than behave like an absent check.
func TestUnconfiguredClientRegistryResolvesNothing(t *testing.T) {
	for _, declaration := range []string{"", "   ", "[]"} {
		registry, err := auth.NewClientRegistry(declaration)
		require.NoError(t, err)
		_, ok := registry.Lookup("example-addin")
		require.False(t, ok)
		require.Empty(t, registry.All())
	}

	var absent *auth.ClientRegistry
	_, ok := absent.Lookup("example-addin")
	require.False(t, ok)
	require.Empty(t, absent.All())
}

func TestRegisteredClientMatchesRedirectURIsExactly(t *testing.T) {
	registry, err := auth.NewClientRegistry(oneClient)
	require.NoError(t, err)
	client, ok := registry.Lookup("example-addin")
	require.True(t, ok)

	require.True(t, client.AllowsRedirect("https://localhost:3000/auth/callback"))
	require.True(t, client.AllowsRedirect("https://addin.example.com/auth/callback"))

	// Every near miss a suffix, prefix, or host comparison would have admitted.
	for _, candidate := range []string{
		"https://addin.example.com/auth/callback/",
		"https://addin.example.com/auth/callback?next=/steal",
		"https://addin.example.com.evil.test/auth/callback",
		"https://evil.test/https://addin.example.com/auth/callback",
		"https://addin.example.com:8443/auth/callback",
		"http://addin.example.com/auth/callback",
		"https://addin.example.com/auth/callback#fragment",
		"",
	} {
		require.False(t, client.AllowsRedirect(candidate), "must not allow %q", candidate)
	}
}

// A malformed declaration fails startup. Dropping the entry it could not read
// would either lock out a working client or, if the unreadable part was a
// constraint, admit more than the operator wrote.
func TestMalformedClientDeclarationsFailClosed(t *testing.T) {
	for name, declaration := range map[string]string{
		"not json":            `{"client_id": "example-addin"}`,
		"no client id":        `[{"name": "X", "redirect_uris": ["https://x.example.com/cb"]}]`,
		"upper-case id":       `[{"client_id": "Example", "name": "X", "redirect_uris": ["https://x.example.com/cb"]}]`,
		"leading dash id":     `[{"client_id": "-example", "name": "X", "redirect_uris": ["https://x.example.com/cb"]}]`,
		"no name":             `[{"client_id": "example", "redirect_uris": ["https://x.example.com/cb"]}]`,
		"no redirect":         `[{"client_id": "example", "name": "X"}]`,
		"confidential kind":   `[{"client_id": "example", "name": "X", "kind": "confidential", "redirect_uris": ["https://x.example.com/cb"]}]`,
		"plaintext redirect":  `[{"client_id": "example", "name": "X", "redirect_uris": ["http://addin.example.com/cb"]}]`,
		"relative redirect":   `[{"client_id": "example", "name": "X", "redirect_uris": ["/auth/callback"]}]`,
		"redirect fragment":   `[{"client_id": "example", "name": "X", "redirect_uris": ["https://x.example.com/cb#f"]}]`,
		"redirect userinfo":   `[{"client_id": "example", "name": "X", "redirect_uris": ["https://u:p@x.example.com/cb"]}]`,
		"custom scheme":       `[{"client_id": "example", "name": "X", "redirect_uris": ["app://callback"]}]`,
		"wildcard origin":     `[{"client_id": "example", "name": "X", "redirect_uris": ["https://x.example.com/cb"], "origins": ["https://*.example.com"]}]`,
		"origin with path":    `[{"client_id": "example", "name": "X", "redirect_uris": ["https://x.example.com/cb"], "origins": ["https://x.example.com/app"]}]`,
		"plaintext origin":    `[{"client_id": "example", "name": "X", "redirect_uris": ["https://x.example.com/cb"], "origins": ["http://x.example.com"]}]`,
		"duplicate client id": `[{"client_id": "example", "name": "X", "redirect_uris": ["https://x.example.com/cb"]},{"client_id": "example", "name": "Y", "redirect_uris": ["https://y.example.com/cb"]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			registry, err := auth.NewClientRegistry(declaration)
			require.Error(t, err)
			require.Nil(t, registry)
		})
	}
}

// HTTP is accepted only for a loopback development host, matching the rule the
// host already applies to its own OAuth callbacks.
func TestLoopbackRedirectsMayUsePlaintextHTTP(t *testing.T) {
	registry, err := auth.NewClientRegistry(
		`[{"client_id": "example-cli", "name": "Example CLI", "redirect_uris": ["http://localhost:7890/callback", "http://127.0.0.1:7890/callback"]}]`)
	require.NoError(t, err)
	client, ok := registry.Lookup("example-cli")
	require.True(t, ok)
	require.True(t, client.AllowsRedirect("http://localhost:7890/callback"))
	require.True(t, client.AllowsRedirect("http://127.0.0.1:7890/callback"))
	require.Empty(t, client.Origins, "a client with no browser context registers no origin")
}

func TestValidClientID(t *testing.T) {
	for _, valid := range []string{"ab", "example-addin", "example_addin", "a1", "x0123456789"} {
		require.True(t, auth.ValidClientID(valid), "%q should be valid", valid)
	}
	for _, invalid := range []string{"", "a", "A", "Example", "-example", "_example", "exa mple", "exa.mple", "exa/mple"} {
		require.False(t, auth.ValidClientID(invalid), "%q should be invalid", invalid)
	}
}
