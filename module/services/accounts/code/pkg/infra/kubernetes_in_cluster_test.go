package infra

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Each fact the kubelet puts in a pod answers "in a cluster" on its own. A pod
// that mounts a service-account token carries the projected files whatever its
// environment says, so the file branch must hold without the variable.
func TestInClusterAnswersYesOnEachKubeletFactAlone(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	ca := filepath.Join(dir, "ca.crt")
	absentToken := filepath.Join(dir, "absent-token")
	absentCA := filepath.Join(dir, "absent-ca.crt")
	require.NoError(t, os.WriteFile(token, []byte("projected"), 0o600))
	require.NoError(t, os.WriteFile(ca, []byte("projected"), 0o600))

	t.Run("service host variable", func(t *testing.T) {
		require.True(t, inCluster("10.96.0.1", absentToken, absentCA))
	})
	t.Run("projected token only", func(t *testing.T) {
		require.True(t, inCluster("", token, absentCA))
	})
	t.Run("projected CA only", func(t *testing.T) {
		require.True(t, inCluster("", absentToken, ca))
	})
	// A path that cannot be checked is not evidence of absence: callers refuse
	// a local-only setting on yes, so the doubt goes to yes.
	t.Run("uncheckable path", func(t *testing.T) {
		require.True(t, inCluster("", filepath.Join(token, "under-a-file"), absentCA))
	})
	// The control: with none of them, the answer is no. Without it an
	// inCluster that always said yes would pass every case above.
	t.Run("none", func(t *testing.T) {
		require.False(t, inCluster("", absentToken, absentCA))
		require.False(t, inCluster("   ", absentToken, absentCA), "a blank variable is not an address")
	})
}

// InCluster wires the real carriers into inCluster: the kubelet's variable and
// its two fixed projected paths, each of which answers on its own.
func TestInClusterReadsTheKubeletCarriers(t *testing.T) {
	require.Equal(t, []string{
		"/var/run/secrets/kubernetes.io/serviceaccount/token",
		"/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
	}, inClusterProjectedPaths)

	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	require.True(t, InCluster())

	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(token, []byte("projected"), 0o600))
	saved := inClusterProjectedPaths
	t.Cleanup(func() { inClusterProjectedPaths = saved })
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	inClusterProjectedPaths = []string{token, filepath.Join(dir, "absent-ca.crt")}
	require.True(t, InCluster(), "a projected token with no variable is still a pod")
	inClusterProjectedPaths = []string{filepath.Join(dir, "absent-token"), filepath.Join(dir, "absent-ca.crt")}
	require.False(t, InCluster())
}
