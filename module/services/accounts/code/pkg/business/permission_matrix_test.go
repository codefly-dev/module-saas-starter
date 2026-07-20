package business_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// TestPermissionMatrix_FrontendCoversBackend — drift guard for the
// FE permissions matrix vs the BE catalog. Fetches the BE set from
// IntrospectionService.GetServiceInfo (canonical) and parses the
// FE matrix (text) from src/lib/permissions.ts. Every permission
// the FE references must exist in the BE catalog; every BE
// resource:action pair must be granted to AT LEAST ONE role in
// the FE (otherwise the FE has no way to display it).
//
// Why text-parse the FE: the alternative is duplicating the BE
// list inline in this test — pointless. Reading the actual file
// catches real drift.
//
// Wildcard handling: `users:*` in the FE counts as a grant for
// every `users:X` BE pair. `*:*` covers everything. `*:read`
// covers any `X:read`. Same semantics as the backend matcher.
func TestPermissionMatrix_FrontendCoversBackend(t *testing.T) {
	// 1. Pull BE catalog. Use authenticated ctx so privileged tiers
	//    are visible — but Permissions list isn't redacted; either
	//    works. Stay consistent with other introspection tests.
	resp, err := testService.GetServiceInfo(authedCtx(), &gen.GetServiceInfoRequest{})
	require.NoError(t, err)

	bePairs := map[string]struct{}{}
	for _, p := range resp.Capabilities.Permissions {
		bePairs[p.Resource+":"+p.Action] = struct{}{}
	}
	require.NotEmpty(t, bePairs, "backend catalog has no permissions")

	// 2. Read FE matrix.
	fePath := frontendPermissionsPath(t)
	raw, err := os.ReadFile(fePath)
	require.NoError(t, err, "failed to read %s", fePath)
	generatedPath := filepath.Join(filepath.Dir(fePath), "..", "gen", "saas", "accounts", "v1", "frontend_catalog.ts")
	generated, err := os.ReadFile(generatedPath)
	require.NoError(t, err, "failed to read %s", generatedPath)

	fePairs := parseFEPermissions(string(raw), string(generated))
	require.NotEmpty(t, fePairs, "FE matrix produced no permissions — parser regex broken?")

	// 3. Every FE pair must exist in BE (allowing wildcards both
	//    sides).
	for fe := range fePairs {
		require.True(t, anyMatch(fe, bePairs),
			"FE permission %q does not appear in the BE catalog. "+
				"Either add it to introspection.go:servicePermissions or fix the FE matrix.", fe)
	}

	// 4. Every BE pair must be reachable via AT LEAST one FE
	//    grant (or via some role's `*:*` / wildcard). Otherwise the
	//    FE has no role that displays this permission's UI.
	for be := range bePairs {
		require.True(t, anyMatch(be, fePairs),
			"BE permission %q is not granted by any role in the FE matrix. "+
				"Add it to permissions.ts (typically the appropriate role array).", be)
	}
}

// frontendPermissionsPath resolves the FE permissions.ts path
// relative to this test's working directory.
func frontendPermissionsPath(t *testing.T) string {
	t.Helper()
	// From pkg/business → walk up to module/services then into frontend.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	for cur := cwd; cur != "/"; cur = filepath.Dir(cur) {
		try := filepath.Join(cur, "module", "services", "frontend", "code", "src", "lib", "permissions.ts")
		if _, err := os.Stat(try); err == nil {
			return try
		}
		// Also try if we're already inside the api code dir:
		try = filepath.Join(cur, "..", "..", "..", "frontend", "code", "src", "lib", "permissions.ts")
		if _, err := os.Stat(try); err == nil {
			abs, _ := filepath.Abs(try)
			return abs
		}
	}
	t.Fatal("cannot locate frontend/code/src/lib/permissions.ts")
	return ""
}

// parseFEPermissions extracts literal grants and resolves generated
// PERMISSIONS.* references used by permissions.ts.
func parseFEPermissions(src, generated string) map[string]struct{} {
	re := regexp.MustCompile(`["']([A-Za-z_*][A-Za-z0-9_]*):([A-Za-z_*][A-Za-z0-9_]*)["']`)
	out := map[string]struct{}{}
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[m[1]+":"+m[2]] = struct{}{}
	}

	constants := map[string]string{}
	constantRE := regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9_]*):\s*["']([A-Za-z_*][A-Za-z0-9_]*:[A-Za-z_*][A-Za-z0-9_]*)["'],?$`)
	for _, match := range constantRE.FindAllStringSubmatch(generated, -1) {
		constants[match[1]] = match[2]
	}
	referenceRE := regexp.MustCompile(`PERMISSIONS\.([A-Z][A-Z0-9_]*)`)
	for _, match := range referenceRE.FindAllStringSubmatch(src, -1) {
		if permission, ok := constants[match[1]]; ok {
			out[permission] = struct{}{}
		}
	}
	return out
}

// anyMatch returns true if `pair` is present in `pool` either
// directly or via wildcard. Both sides may carry `*`. Mirrors the
// scopeMatches semantics in adapters/auth.go.
func anyMatch(pair string, pool map[string]struct{}) bool {
	if _, ok := pool[pair]; ok {
		return true
	}
	res, act := splitColon(pair)
	for k := range pool {
		kr, ka := splitColon(k)
		if (kr == "*" || kr == res) && (ka == "*" || ka == act) {
			return true
		}
		if (res == "*" || kr == res) && (act == "*" || ka == act) {
			return true
		}
	}
	return false
}

func splitColon(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

var _ = context.Background // keep import for parity with other tests
