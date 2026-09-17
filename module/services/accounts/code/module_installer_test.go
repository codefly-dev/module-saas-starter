package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModuleInstallerPolicyConfiguration(t *testing.T) {
	t.Setenv("MODULE_INSTALLER_POLICY_FILE", "")
	handler, err := configuredModuleInstaller(nil)
	require.NoError(t, err)
	require.Nil(t, handler, "no policy file means no installer surface")

	t.Setenv("MODULE_INSTALLER_POLICY_FILE", filepath.Join(t.TempDir(), "missing"))
	handler, err = configuredModuleInstaller(nil)
	require.Error(t, err)
	require.Nil(t, handler)

	path := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":"unknown"}`), 0600))
	t.Setenv("MODULE_INSTALLER_POLICY_FILE", path)
	_, err = configuredModuleInstaller(nil)
	require.Error(t, err)
}
