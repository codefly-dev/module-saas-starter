package frontendpayload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/composition"
	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

func TestDeclaredBundleClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, *solutionhost.Frontend)
	}{
		{"document relative root", func(m map[string]any, _ *solutionhost.Frontend) { m["metaData"].(map[string]any)["publicPath"] = "./" }},
		{"executable root", func(m map[string]any, _ *solutionhost.Frontend) {
			m["metaData"].(map[string]any)["getPublicPath"] = `function(){return "/mutable/"}`
		}},
		{"missing root", func(m map[string]any, _ *solutionhost.Frontend) { delete(m["metaData"].(map[string]any), "publicPath") }},
		{"external remote", func(m map[string]any, _ *solutionhost.Frontend) {
			m["remotes"] = []any{map[string]any{"entry": "https://example.com/entry.js"}}
		}},
		{"missing expose", func(_ map[string]any, f *solutionhost.Frontend) { f.ExposedModule = "./Missing" }},
		{"missing surface", func(_ map[string]any, f *solutionhost.Frontend) {
			f.Surfaces = []solutionhost.FrontendSurface{{Module: "/assets/missing.js"}}
		}},
		{"surface outside assets", func(_ map[string]any, f *solutionhost.Frontend) {
			f.Surfaces = []solutionhost.FrontendSurface{{Module: "/deferred.js"}}
		}},
		{"entry escapes", func(m map[string]any, _ *solutionhost.Frontend) {
			m["metaData"].(map[string]any)["remoteEntry"].(map[string]any)["path"] = "../"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { checkChangedManifest(t, tc.change) })
	}
	for _, owner := range []string{"exposes", "shared"} {
		for _, kind := range []string{"js", "css"} {
			for _, timing := range []string{"sync", "async"} {
				t.Run(owner+kind+timing, func(t *testing.T) {
					checkChangedManifest(t, func(m map[string]any, _ *solutionhost.Frontend) {
						m[owner] = []any{map[string]any{"path": "./Page", "assets": map[string]any{kind: map[string]any{timing: []string{"missing.js"}}}}}
					})
				})
			}
		}
	}
}

func checkChangedManifest(t *testing.T, change func(map[string]any, *solutionhost.Frontend)) {
	t.Helper()
	data := archiveFor(t, "good")
	f := frontendFor(data)
	source := memorySource{f.Payload.Digest: data}
	good, err := Prepare(t.Context(), source, f)
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(good.files["mf-manifest.json"], &manifest))
	change(manifest, f)
	dir := t.TempDir()
	for name, content := range good.files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), content, 0600))
	}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mf-manifest.json"), raw, 0600))
	data, _, err = composition.CanonicalArchive(dir)
	require.NoError(t, err)
	f.Payload = frontendFor(data).Payload
	_, err = Prepare(t.Context(), memorySource{f.Payload.Digest: data}, f)
	require.Error(t, err)
}

func TestPreparationMatchesCompleteDeclaration(t *testing.T) {
	data := archiveFor(t, "good")
	f := frontendFor(data)
	f.Surfaces = []solutionhost.FrontendSurface{{Module: "/assets/deferred.js"}}
	bundle, err := Prepare(t.Context(), memorySource{f.Payload.Digest: data}, f)
	require.NoError(t, err)
	require.True(t, bundle.Matches(f))
	f.Surfaces[0].Module = "/assets/missing.js"
	require.False(t, bundle.Matches(f))
}

func TestSparseExpansionRefusedBeforeExtraction(t *testing.T) {
	data, err := os.ReadFile("testdata/sparse.tar")
	require.NoError(t, err)
	f := frontendFor(data)
	_, err = Prepare(t.Context(), memorySource{f.Payload.Digest: data}, f)
	require.ErrorContains(t, err, "expanded size limit")
}
