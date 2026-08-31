package main

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
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

// The auth-gateway is the request perimeter: every request into the mesh is
// authorized here by Envoy ext_authz. The invariant these tests lock is that
// the perimeter stays WorkOS-free — the only trust anchor inside the mesh is
// our own Ed25519 keypair, and the only identity that reaches downstream
// services is the canonical x-* header set. WorkOS is an edge-only concern
// (accounts, once, at sign-in code exchange). A regression that pulls an
// external IdP into the perimeter would break BYOC (an external IdP call
// inside a tenant's perimeter) and make WorkOS un-swappable; one of these
// tests must fail before that can land.

// serviceConfigPath returns the absolute path to auth-gateway's
// service.codefly.yaml (one directory above the code/ package).
func serviceConfigPath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(filepath.Dir(currentFile)), "service.codefly.yaml")
}

// allowedPerimeterConfigDeps is the frozen set of workspace configuration
// groups the perimeter may depend on: internal-auth carries the Ed25519 trust
// anchor + internal tokens, and gateway/observability/security carry no
// external-IdP material. `identity` — the group accounts reads WorkOS/authkit
// values from — is deliberately absent, as is any future provider group under
// any name. Asserting deps are a SUBSET of this set (not merely != "identity")
// means a WorkOS config group added under a different name still trips the test;
// a legitimate new dependency forces a human to add it here and eyeball it.
var allowedPerimeterConfigDeps = map[string]bool{
	"gateway":       true,
	"internal-auth": true,
	"observability": true,
	"security":      true,
}

// TestPerimeter_WorkspaceConfigExcludesIdentity asserts the sidecar's
// workspace-configuration-dependencies stay within the allowlist of
// external-IdP-free config groups, and in particular never pull in `identity` —
// the group that carries WorkOS/authkit credentials. internal-auth (the Ed25519
// trust anchor + internal tokens) is what the perimeter depends on for its trust
// material; identity would route an external IdP's configuration into the mesh.
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
	for _, dep := range cfg.WorkspaceConfigurationDependencies {
		require.Truef(t, allowedPerimeterConfigDeps[dep],
			"perimeter depends on unlisted config group %q: any group outside allowedPerimeterConfigDeps risks routing external-IdP config into the mesh; add it to the allowlist only after confirming it carries no external-IdP material", dep)
	}
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

	// A token with a foreign (WorkOS/authkit) issuer is denied even though it
	// is otherwise well-formed: the verifier is locked to our own issuer, so a
	// provider-issued token is never accepted regardless of how it was signed.
	foreign := validClaims(now)
	foreign.Issuer = "https://api.workos.com/user_management/authkit"
	resp, err = s.Check(ctx, checkReq("/v1/users", map[string]string{"authorization": "Bearer " + signClaims(t, priv, foreign)}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "a token issued by a WorkOS/authkit issuer must be denied")
}

// fileImports returns the import paths of a single Go source file.
func fileImports(t *testing.T, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	require.NoError(t, err)
	paths := make([]string, 0, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
		require.NoError(t, unquoteErr)
		paths = append(paths, importPath)
	}
	return paths
}

// httpAllowedPerimeterFiles is the allowlist of package-main source files
// permitted to import net/http. The sidecar hosts an HTTP gateway plus its
// rate-limit and telemetry surfaces, which legitimately speak HTTP. The request
// verifier — sidecar.go and any NEW same-package helper checkJWT reaches — is
// deliberately absent: it verifies own tokens with local Ed25519 crypto and its
// only permitted network call is the api-key backend gRPC. Adding a file here
// forces a human to justify a new HTTP surface in the perimeter and confirm it
// carries no external-IdP call in the request path.
var httpAllowedPerimeterFiles = map[string]bool{
	"gateway.go":           true,
	"gateway_solutions.go": true,
	"main.go":              true,
	"ratelimit.go":         true,
	"telemetry_metrics.go": true,
}

// TestPerimeter_NoExternalIdPImports guards ask #2 of issue #313 structurally.
//
// net/http scan: every package-main production file EXCEPT the allowlisted HTTP
// surfaces must not import net/http. Scoping to sidecar.go alone would miss the
// real regression this closes: a JWKS/HTTP key-fetch added in a NEW file (e.g. a
// jwks.go that pulls the verification key from an external IdP endpoint) reached
// from checkJWT — such a file leaves issuer and emitted headers unchanged, so no
// behavioral test catches it, and a generic HTTP-client import path carries no
// "workos"/"authkit" substring for the scan below to catch either. Pinning the
// permitted-importer set to a frozen allowlist means any other file pulling in
// net/http fails here. The scan is the top-level main package only: the verifier
// and its same-package helpers live there, while the generated grpc-gateway code
// under pkg/gen legitimately imports net/http and is not hand-edited.
//
// workos/authkit scan: NO file anywhere in the perimeter service may import a
// WorkOS/authkit package. Those substrings never legitimately appear in an
// import path here, so this scan stays package-wide (the whole tree), closing
// the gap where a provider client is added via a helper in a new file or
// subpackage.
func TestPerimeter_NoExternalIdPImports(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	codeDir := filepath.Dir(currentFile)

	entries, err := os.ReadDir(codeDir)
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") || httpAllowedPerimeterFiles[name] {
			continue
		}
		for _, imp := range fileImports(t, filepath.Join(codeDir, name)) {
			require.NotContains(t, strings.ToLower(imp), "net/http",
				"%s imports %q but is not on httpAllowedPerimeterFiles: the perimeter verifies own tokens with local crypto and must not reach an external IdP over HTTP; if this file legitimately needs net/http, add it to the allowlist only after confirming it carries no external-IdP call in the request path", name, imp)
		}
	}

	err = filepath.WalkDir(codeDir, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		for _, imp := range fileImports(t, path) {
			lower := strings.ToLower(imp)
			require.NotContains(t, lower, "workos",
				"%s imports %q: the perimeter must stay WorkOS-free", path, imp)
			require.NotContains(t, lower, "authkit",
				"%s imports %q: the perimeter must stay WorkOS-free", path, imp)
		}
		return nil
	})
	require.NoError(t, err)
}

// wantEmittedHeaders is a FROZEN, hand-maintained snapshot of every header the
// ext_authz allow decision is permitted to emit on the JWT path. It is
// deliberately NOT derived from canonicalUpstreamAuthHeaders: deriving the
// expectation from the same variable the code emits from makes the check
// circular, so adding a provider header (e.g. x-workos-sub) to that variable
// would silently satisfy the test. Because allow() emits every canonical
// header (empty values included) plus the gateway token, the emitted key set
// is fully deterministic and can be pinned exactly. Any drift — a new header,
// a rename, a provider leak — forces a human to edit this list and eyeball the
// change, which is the whole point of ask #3 in issue #313.
var wantEmittedHeaders = []string{
	"x-user-id", "x-org-id", "x-org-role", "x-platform-role", "x-roles",
	"x-scoped-roles", "x-scoped-roles-truncated", "x-auth-id", "x-user-email",
	"x-user-name", "x-session-id", "x-acting-as-user-id", "x-act", "x-scopes",
	"x-mfa-satisfied", "x-authentication-methods", "x-auth-time",
	"x-assurance-level", "x-mfa-verified-at", "x-codefly-gateway-token",
}

// TestPerimeter_SuccessPathEmitsCanonicalHeadersOnly asserts the ext_authz
// allow decision projects exactly the frozen canonical x-* identity header
// set, never forwards the caller's bearer token, and replaces any spoofed
// provider-shaped identity a client tries to smuggle in with the canonical
// UUID from the verified claims.
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

	emitted := headerMap(resp)

	// Exact set equality against the frozen list: a provider header added to
	// the emit path shows up as an unexpected key here, and a canonical header
	// dropped shows up as a missing one. Neither can pass silently.
	emittedKeys := make([]string, 0, len(emitted))
	for key := range emitted {
		emittedKeys = append(emittedKeys, strings.ToLower(key))
	}
	require.ElementsMatch(t, wantEmittedHeaders, emittedKeys,
		"success path emitted a header set that drifted from the frozen canonical list — a provider leak or an untracked header change; update wantEmittedHeaders only after confirming the new header carries no provider identity")

	for key, value := range emitted {
		require.NotContains(t, value, token,
			"success path must never forward the caller's bearer token (header %q)", key)
		require.NotContains(t, value, spoofedWorkOSSub,
			"success path must never forward a provider-shaped identity (header %q)", key)
	}

	require.Equal(t, claims.Subject, emitted["x-user-id"],
		"x-user-id must carry the canonical UUID from the verified claims, not a client-supplied value")
}
