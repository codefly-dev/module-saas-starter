package main

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The auth-sidecar is the request perimeter: every request into the mesh is
// authorized here by Envoy ext_authz. The invariant these tests lock is that
// the perimeter stays WorkOS-free — the only trust anchor inside the mesh is
// our own Ed25519 keypair, and the only identity that reaches downstream
// services is the canonical x-* header set. WorkOS is an edge-only concern
// (accounts, once, at sign-in code exchange). A regression that pulls an
// external IdP into the perimeter would break BYOC (an external IdP call
// inside a tenant's perimeter) and make WorkOS un-swappable; one of these
// tests must fail before that can land.

// serviceConfigPath returns the absolute path to auth-sidecar's
// service.codefly.yaml (one directory above the code/ package).
func serviceConfigPath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(filepath.Dir(currentFile)), "service.codefly.yaml")
}

// TestPerimeter_WorkspaceConfigExcludesIdentity asserts the sidecar's
// workspace-configuration-dependencies never pull in `identity` — the config
// group that carries WorkOS/authkit credentials. internal-auth (the Ed25519
// trust anchor + internal tokens) is what the perimeter is allowed to depend
// on; identity would route an external IdP's configuration into the perimeter.
func TestPerimeter_WorkspaceConfigExcludesIdentity(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(serviceConfigPath(t))
	require.NoError(t, err)

	var cfg struct {
		WorkspaceConfigurationDependencies []string `yaml:"workspace-configuration-dependencies"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))

	require.Contains(t, cfg.WorkspaceConfigurationDependencies, "internal-auth",
		"perimeter must depend on internal-auth for the Ed25519 trust anchor")
	require.NotContains(t, cfg.WorkspaceConfigurationDependencies, "identity",
		"perimeter must NOT depend on identity: that would route WorkOS/authkit config into the mesh perimeter")
}

// TestPerimeter_VerifierIsLocalOwnTokenOnly asserts the request-path token
// verifier is anchored on our own issuer + a local Ed25519 public key, and
// that verification needs no network — a valid own-token is accepted with a
// sidecar that has no backend connection, and a token minted by a WorkOS-style
// issuer is rejected.
func TestPerimeter_VerifierIsLocalOwnTokenOnly(t *testing.T) {
	t.Parallel()

	s, priv := newTestSidecar(t)
	// A sidecar constructed via the real constructor is anchored on our own
	// issuer/audience — not a provider's.
	real := NewSidecar(nil, s.publicKey)
	require.Equal(t, "saas-starter", real.issuer)
	require.Equal(t, "saas-starter", real.audience)

	ctx := context.Background()
	now := time.Now()

	// Own token, verified with a local public key and no network available
	// (nil api-key conn, noop revoker): must be admitted.
	own := signClaims(t, priv, validClaims(now))
	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{"authorization": "Bearer " + own}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "own Ed25519 token must verify locally with no network call")

	// A token whose issuer is a WorkOS/authkit endpoint must be rejected: the
	// verifier is locked to our own issuer, so it can never trust a provider's
	// JWKS-signed token even if the signature matched.
	foreign := validClaims(now)
	foreign.Issuer = "https://api.workos.com/user_management/authkit"
	resp, err = s.Check(ctx, checkReq("/v1/users", map[string]string{"authorization": "Bearer " + signClaims(t, priv, foreign)}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "a token issued by a WorkOS/authkit issuer must be denied")
}

// TestPerimeter_RequestPathHasNoHTTPClient asserts the ext_authz request-path
// source (sidecar.go — Check/checkJWT/checkAPIKey) imports no HTTP client and
// no WorkOS/JWKS-over-HTTP package. The Ed25519 public key is fetched once at
// startup from the accounts backend (main.go), never from a provider and never
// on the request hot path.
func TestPerimeter_RequestPathHasNoHTTPClient(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	sidecarSrc := filepath.Join(filepath.Dir(currentFile), "sidecar.go")

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, sidecarSrc, nil, parser.ImportsOnly)
	require.NoError(t, err)

	forbidden := []string{"net/http", "workos", "authkit"}
	for _, imported := range parsed.Imports {
		importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
		require.NoError(t, unquoteErr)
		for _, bad := range forbidden {
			require.NotContains(t, strings.ToLower(importPath), bad,
				"request-path source must not import %q: the perimeter verifies own tokens with local crypto only", importPath)
		}
	}
}

// TestPerimeter_SuccessPathEmitsCanonicalHeadersOnly asserts the ext_authz
// allow decision projects only the canonical x-* identity headers, never
// forwards the caller's bearer token, and replaces any spoofed provider-shaped
// identity a client tries to smuggle in with the canonical UUID from the
// verified claims.
func TestPerimeter_SuccessPathEmitsCanonicalHeadersOnly(t *testing.T) {
	t.Parallel()

	s, priv := newTestSidecar(t)
	ctx := context.Background()

	claims := validClaims(time.Now())
	token := signClaims(t, priv, claims)

	// The client presents a valid own-token but also tries to smuggle a
	// WorkOS-style user id in the canonical header.
	const spoofedWorkOSSub = "user_01H9WORKOSSUBSHOULDNEVERLEAK"
	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
		"x-user-id":     spoofedWorkOSSub,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())

	allowed := map[string]bool{"x-codefly-gateway-token": true}
	for _, h := range canonicalUpstreamAuthHeaders {
		allowed[h] = true
	}

	emitted := headerMap(resp)
	for key, value := range emitted {
		require.True(t, allowed[strings.ToLower(key)],
			"success path emitted non-canonical header %q — only canonical x-* identity headers may reach downstream", key)
		require.NotContains(t, value, token,
			"success path must never forward the caller's bearer token (header %q)", key)
		require.NotContains(t, value, spoofedWorkOSSub,
			"success path must never forward a provider-shaped identity (header %q)", key)
	}

	require.Equal(t, claims.Subject, emitted["x-user-id"],
		"x-user-id must carry the canonical UUID from the verified claims, not a client-supplied value")
	require.NotContains(t, emitted, "authorization",
		"the caller's Authorization/bearer credential must not be forwarded upstream")
}
