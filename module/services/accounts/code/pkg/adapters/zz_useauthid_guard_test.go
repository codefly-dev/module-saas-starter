package adapters

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGuard_NoRawUserAuthIDInRPCHandlers keeps RPC handlers from reading the
// caller id straight off wool's UserAuthID() (X-Auth-Id / user.auth.id). Behind
// the Envoy auth-sidecar the gateway stamps only X-User-Id and forwards a blank
// user.auth.id, so a raw UserAuthID() read collapses to an empty actor that
// flows into uuid-typed SQL and 500s (see #121). Handlers must resolve the actor
// through requireAuth(ctx), which prefers UserID() and rejects empties.
//
// The scan is scoped to the *rpcs.go handler files. The auth helpers
// (requireAuth/callerID in auth.go), the interceptors, and the Connect plumbing
// legitimately consult UserAuthID() and are out of scope.
func TestGuard_NoRawUserAuthIDInRPCHandlers(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "rpcs.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		if strings.Contains(string(src), ".UserAuthID(") {
			offenders = append(offenders, name)
		}
	}

	require.Empty(t, offenders,
		"RPC handlers must resolve the actor via requireAuth(ctx), not a raw w.UserAuthID() read (empty behind the auth-sidecar; see #121)")
}
