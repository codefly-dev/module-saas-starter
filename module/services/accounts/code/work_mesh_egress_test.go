package main

import (
	"errors"
	"net/netip"
	"testing"

	"accounts/pkg/auth"

	"github.com/stretchr/testify/require"
)

// meshEgressSource is the internal-transport group as a deployment delivers it.
type meshEgressSource map[string]string

func (s meshEgressSource) WorkspaceValue(group, key string) (string, error) {
	if value, ok := s[group+"/"+key]; ok {
		return value, nil
	}
	return "", errors.New("absent")
}

func withdrawMeshEgress(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = auth.ConfigureMeshEgress(false, "", "") })
}

func admitsDeclaredMeshAddress() bool {
	return auth.AdmitsDestination("api.declared.example:443", netip.MustParseAddr("240.240.0.5"))
}

// A meshed deployment that declares its egress hosts admits them at the mesh's
// default egress range; nothing else changes.
func TestConfigureMeshEgressInstallsTheDeclaredHosts(t *testing.T) {
	withdrawMeshEgress(t)
	require.NoError(t, configureMeshEgress(meshEgressSource{
		"internal-transport/mesh-protected":    "true\n",
		"internal-transport/mesh-egress-hosts": "api.declared.example",
	}))
	require.True(t, admitsDeclaredMeshAddress())
	require.False(t, auth.AdmitsDestination("api.undeclared.example:443", netip.MustParseAddr("240.240.0.5")))
}

// Declared hosts without the mesh assertion refuse startup, naming the keys;
// so does a mesh assertion that is neither true nor false. Neither installs
// anything.
func TestConfigureMeshEgressRefusesWhatItCannotInstall(t *testing.T) {
	withdrawMeshEgress(t)
	err := configureMeshEgress(meshEgressSource{"internal-transport/mesh-egress-hosts": "api.declared.example"})
	require.ErrorIs(t, err, auth.ErrMeshEgressWithoutMesh)
	require.Contains(t, err.Error(), "internal-transport/mesh-egress-hosts")
	require.False(t, admitsDeclaredMeshAddress())

	err = configureMeshEgress(meshEgressSource{
		"internal-transport/mesh-protected":    "true",
		"internal-transport/mesh-egress-hosts": "api.declared.example",
		"internal-transport/mesh-egress-range": "10.0.0.0/8",
	})
	require.ErrorIs(t, err, auth.ErrInvalidMeshEgress)
	require.False(t, admitsDeclaredMeshAddress())

	require.Error(t, configureMeshEgress(meshEgressSource{"internal-transport/mesh-protected": "yes"}))

	// Nothing declared, the default everywhere today: nothing installed.
	require.NoError(t, configureMeshEgress(meshEgressSource{}))
	require.False(t, admitsDeclaredMeshAddress())
}

// The refusal is reached through the function main calls.
func TestStartupRefusesMeshEgressWithoutTheMesh(t *testing.T) {
	withdrawMeshEgress(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "false")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_EGRESS_HOSTS", "api.declared.example")
	err := requireStartupConfiguration(t.Context(), false)
	require.ErrorIs(t, err, auth.ErrMeshEgressWithoutMesh)
}
