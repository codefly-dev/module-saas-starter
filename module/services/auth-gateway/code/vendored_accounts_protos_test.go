package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVendoredAccountsProtosAreAccounts holds every accounts proto this gateway
// generates from byte-identical to the file accounts owns. The gateway carries
// only the files whose imports it can resolve (workload_operation_admission.proto,
// module_registration.proto, solution_entitlements.proto and their peers), and
// accounts keeps those files import-minimal for that reason. A copy that
// drifted would decode a different wire shape from the one accounts writes,
// silently, and would also stop the two published contracts from loading into
// one descriptor pool.
func TestVendoredAccountsProtosAreAccounts(t *testing.T) {
	vendored, err := filepath.Glob(filepath.Join("..", "proto", "saas", "accounts", "v1", "*.proto"))
	require.NoError(t, err)
	require.NotEmpty(t, vendored)
	for _, path := range vendored {
		copied, err := os.ReadFile(path)
		require.NoError(t, err)
		owned, err := os.ReadFile(filepath.Join("..", "..", "accounts", "proto", "saas", "accounts", "v1", filepath.Base(path)))
		require.NoError(t, err, "%s has no accounts-owned original", filepath.Base(path))
		require.True(t, bytes.Equal(owned, copied), "%s drifted from accounts' file of the same name", filepath.Base(path))
	}
}
