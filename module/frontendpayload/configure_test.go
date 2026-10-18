package frontendpayload

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type workspaceValues map[string]string

func (v workspaceValues) WorkspaceValueIfSet(group, key string) (string, bool, error) {
	if group != "artifact-cache" || (key != "ROOT" && key != "REPOSITORY") {
		panic("unexpected lookup")
	}
	value, ok := v[key]
	return value, ok, nil
}

func TestNativeSourceConfiguration(t *testing.T) {
	values := workspaceValues{"ROOT": t.TempDir()}
	source, err := Configure(t.Context(), values, true, false)
	require.NoError(t, err)
	require.IsType(t, &DirectorySource{}, source)
	for _, mode := range [][2]bool{{false, false}, {true, true}, {false, true}} {
		_, err := Configure(t.Context(), values, mode[0], mode[1])
		require.Error(t, err)
	}
	source, err = Configure(t.Context(), workspaceValues{}, false, true)
	require.NoError(t, err)
	require.Nil(t, source)
}

// A deployed Pod reads the repository the render published to, as itself; the
// repository is refused outside a Pod, beside a native root, and on a registry
// this host cannot read as its workload identity.
func TestPublishedSourceConfiguration(t *testing.T) {
	published := workspaceValues{"REPOSITORY": "us-east1-docker.pkg.dev/project/registry/platform/frontend-bundles"}
	source, err := Configure(t.Context(), published, false, true)
	require.NoError(t, err)
	require.IsType(t, &RegistrySource{}, source)
	repository := source.(*RegistrySource).repository
	require.False(t, repository.PlainHTTP, "a published repository is read over HTTPS")
	require.Equal(t, "us-east1-docker.pkg.dev", repository.Reference.Registry)
	for _, mode := range [][2]bool{{true, false}, {false, false}, {true, true}} {
		_, err := Configure(t.Context(), published, mode[0], mode[1])
		require.ErrorContains(t, err, "deployed Pod")
	}
	_, err = Configure(t.Context(), workspaceValues{"REPOSITORY": published["REPOSITORY"], "ROOT": t.TempDir()}, false, true)
	require.ErrorContains(t, err, "one source")
	_, err = Configure(t.Context(), workspaceValues{"REPOSITORY": "registry.example/frontend-bundles"}, false, true)
	require.ErrorContains(t, err, "workload identity")
}
