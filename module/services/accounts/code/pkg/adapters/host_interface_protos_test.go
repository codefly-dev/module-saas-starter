package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The interface protos under proto/host are a copy of the published ones
// (codefly-dev/authority) until the CLI generates from a declared interface.
// Two copies are the transition; a copy that drifts silently would be the
// defect. DIGESTS.sha256 records the published files' digests; a change on
// either side fails here by file name.
func TestCopiedInterfaceProtosMatchTheirRecordedDigests(t *testing.T) {
	root := filepath.Join("..", "..", "..", "proto", "host")
	recorded, err := os.ReadFile(filepath.Join(root, "DIGESTS.sha256"))
	require.NoError(t, err)
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		require.True(t, ok, "line %q", line)
		want[name] = digest
	}
	seen := map[string]bool{}
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		require.Equal(t, want[rel], hex.EncodeToString(sum[:]), "%s differs from the published interface; re-copy from codefly-dev/authority and record the new digest", rel)
		seen[rel] = true
		return nil
	}))
	for name := range want {
		require.True(t, seen[name], "%s is recorded but missing", name)
	}
}
