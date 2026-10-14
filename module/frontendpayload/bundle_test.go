package frontendpayload

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

type memorySource map[solutionhost.PayloadDigest][]byte

func (s memorySource) Open(_ context.Context, digest solutionhost.PayloadDigest) (io.ReadCloser, error) {
	data, ok := s[digest]
	if !ok {
		return nil, errors.New("private-source?credential=must-not-escape")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func frontendFor(data []byte) *solutionhost.Frontend {
	return &solutionhost.Frontend{HostContract: 1, ExposedModule: "./Page", Payload: &solutionhost.FrontendPayload{
		Artifact: "frontend", MediaType: "application/x-tar",
		Digest: solutionhost.PayloadDigest(fmt.Sprintf("sha256:%x", sha256.Sum256(data))),
	}}
}

func archiveFor(t *testing.T, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"mf-manifest.json": `{"metaData":{"globalName":"example","publicPath":"auto","remoteEntry":{"name":"remoteEntry.js","path":"","type":"global"}},"exposes":[{"path":"./Page","assets":{"js":{"sync":["remoteEntry.js"],"async":["deferred.js"]}}}]}`,
		"remoteEntry.js":   "import('./deferred.js')", "deferred.js": version,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0600))
	}
	data, _, err := composition.CanonicalArchive(dir)
	require.NoError(t, err)
	return data
}

func readAsset(t *testing.T, b *Bundle, name string) string {
	t.Helper()
	r, size, err := b.Open(name)
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.EqualValues(t, len(data), size)
	return string(data)
}

func TestCompleteSnapshotsSurviveSourceMutationAndRecoverIndependently(t *testing.T) {
	a, b := archiveFor(t, "version-a"), archiveFor(t, "version-b")
	fa, fb := frontendFor(a), frontendFor(b)
	source := memorySource{fa.Payload.Digest: a, fb.Payload.Digest: b}
	first, err := Prepare(t.Context(), source, fa)
	require.NoError(t, err)
	second, err := Prepare(t.Context(), source, fb)
	require.NoError(t, err)
	recovered, err := Prepare(t.Context(), source, fa)
	require.NoError(t, err)
	require.True(t, first.Matches(fa))
	require.False(t, first.Matches(fb))
	// Corrupt just the deferred file without changing the manifest or entry.
	index := bytes.Index(a, []byte("version-a"))
	require.GreaterOrEqual(t, index, 0)
	a[index] = 'X'
	_, err = Prepare(t.Context(), source, fa)
	require.ErrorContains(t, err, "digest mismatch")
	for _, snapshot := range []*Bundle{first, recovered} {
		require.Equal(t, "version-a", readAsset(t, snapshot, "deferred.js"))
	}
	require.Equal(t, "version-b", readAsset(t, second, "deferred.js"))
	// A missing A cannot silently acquire B, including after cache loss.
	delete(source, fa.Payload.Digest)
	_, err = Prepare(t.Context(), source, fa)
	require.ErrorIs(t, err, ErrUnavailable)
	require.NotContains(t, err.Error(), "credential")
	require.Equal(t, "version-a", readAsset(t, first, "deferred.js"))
}

func TestPreparationRefusesUnsafeOrIncompleteArchives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../escape", Mode: 0600}}},
		{"absolute", []*tar.Header{{Name: "/escape", Mode: 0600}}},
		{"symlink", []*tar.Header{{Name: "entry", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}},
		{"hardlink", []*tar.Header{{Name: "entry", Typeflag: tar.TypeLink, Linkname: "other"}}},
		{"duplicate", []*tar.Header{{Name: "same", Mode: 0600}, {Name: "same", Mode: 0600}}},
		{"no manifest", []*tar.Header{{Name: "remoteEntry.js", Mode: 0600}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			for _, header := range tc.headers {
				require.NoError(t, writer.WriteHeader(header))
			}
			require.NoError(t, writer.Close())
			frontend := frontendFor(data.Bytes())
			_, err := Prepare(t.Context(), memorySource{frontend.Payload.Digest: data.Bytes()}, frontend)
			require.Error(t, err)
		})
	}
}

func TestBundleBoundary(t *testing.T) {
	data := archiveFor(t, "a")
	frontend := frontendFor(data)
	source := memorySource{frontend.Payload.Digest: data}
	bundle, err := Prepare(t.Context(), source, frontend)
	require.NoError(t, err)
	for _, name := range []string{"../deferred.js", "/deferred.js", "a/../deferred.js", `%2e%2e/deferred.js`, `a\deferred.js`, "missing.js"} {
		_, _, err := bundle.Open(name)
		require.ErrorIs(t, err, fs.ErrNotExist, name)
	}
	for _, mutate := range []func(*solutionhost.Frontend){
		func(f *solutionhost.Frontend) { f.HostContract++ },
		func(f *solutionhost.Frontend) { f.Payload.MediaType = "application/gzip" },
		func(f *solutionhost.Frontend) { f.Payload = nil },
	} {
		candidate := frontendFor(data)
		mutate(candidate)
		_, err := Prepare(t.Context(), source, candidate)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Prepare(ctx, source, frontend)
	require.ErrorIs(t, err, context.Canceled)
	large := make([]byte, MaxBundleBytes+1)
	largeFrontend := frontendFor(large)
	_, err = Prepare(t.Context(), memorySource{largeFrontend.Payload.Digest: large}, largeFrontend)
	require.ErrorContains(t, err, "size limit")
}
