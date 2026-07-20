package infra_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGuard_NoRawPoolMutationsInTests enforces the RLS-aware-tests rule: tests
// must not INSERT/UPDATE/DELETE through the raw testPool (an app_tenant
// connection with no RLS context — which fail-closes or, worse, masks RLS).
// Seeds and verification go through As(Identity{...}) / WithControlPlane so RLS is
// actually exercised. Scans every *_test.go under code/pkg.
func TestGuard_NoRawPoolMutationsInTests(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	pkgRoot := filepath.Dir(filepath.Dir(thisFile)) // .../code/pkg

	mutations := []string{"INSERT", "UPDATE ", "DELETE FROM"}
	var offenders []string

	require.NoError(t, filepath.WalkDir(pkgRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		if strings.HasSuffix(path, "zz_rls_test_guard_test.go") {
			return nil
		}
		// billing/pg deliberately resolves the test-only migration-owner
		// capability through the Codefly SDK to unit-test billing store SQL with
		// RLS out of scope. RLS is covered by the rls_*_test.go suites.
		if strings.Contains(filepath.ToSlash(path), "/billing/pg/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(src), "\n")
		for i, ln := range lines {
			if !strings.Contains(ln, "testPool.Exec(") {
				continue
			}
			// Look at this line + the next 5 (the SQL often spans lines).
			window := strings.ToUpper(strings.Join(lines[i:min(i+6, len(lines))], "\n"))
			for _, m := range mutations {
				if strings.Contains(window, m) {
					offenders = append(offenders, path)
				}
			}
		}
		return nil
	}))

	require.Empty(t, offenders,
		"tests must not mutate via raw testPool (bypasses RLS context); seed/verify via As(Identity{...}) or WithControlPlane")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
