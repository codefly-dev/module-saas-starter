package frontendpayload

import (
	"github.com/stretchr/testify/require"
	"testing"
)

type workspaceValues string

func (v workspaceValues) WorkspaceValueIfSet(group, key string) (string, bool, error) {
	if group != "artifact-cache" || key != "ROOT" {
		panic("unexpected lookup")
	}
	return string(v), v != "", nil
}

func TestNativeSourceConfiguration(t *testing.T) {
	values := workspaceValues(t.TempDir())
	source, err := ConfigureNative(t.Context(), values, true, false)
	require.NoError(t, err)
	require.NotNil(t, source)
	for _, mode := range [][2]bool{{false, false}, {true, true}, {false, true}} {
		_, err := ConfigureNative(t.Context(), values, mode[0], mode[1])
		require.Error(t, err)
	}
	source, err = ConfigureNative(t.Context(), workspaceValues(""), false, true)
	require.NoError(t, err)
	require.Nil(t, source)
}
