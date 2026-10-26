package main

import (
	"errors"
	"net/netip"
	"testing"

	"accounts/pkg/auth"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// meshEgressSource is the internal-transport and egress groups as a deployment
// delivers them.
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
		"internal-transport/mesh-protected": "true\n",
		"egress/HOSTS":                      "api.declared.example:443",
	}))
	require.True(t, admitsDeclaredMeshAddress())
	require.False(t, auth.AdmitsDestination("api.undeclared.example:443", netip.MustParseAddr("240.240.0.5")))
}

// Declared hosts without the mesh assertion start the service and admit
// nothing. A range without it refuses startup, naming the keys; so does a mesh
// assertion that is neither true nor false. Neither installs anything.
func TestConfigureMeshEgressRefusesWhatItCannotInstall(t *testing.T) {
	withdrawMeshEgress(t)
	require.NoError(t, configureMeshEgress(meshEgressSource{"egress/HOSTS": "api.declared.example:443"}))
	require.False(t, admitsDeclaredMeshAddress())

	err := configureMeshEgress(meshEgressSource{
		"egress/HOSTS":                         "api.declared.example:443",
		"internal-transport/mesh-egress-range": "240.240.0.0/16",
	})
	require.ErrorIs(t, err, auth.ErrMeshEgressWithoutMesh)
	require.Contains(t, err.Error(), "internal-transport/mesh-egress-range")
	require.False(t, admitsDeclaredMeshAddress())

	err = configureMeshEgress(meshEgressSource{
		"internal-transport/mesh-protected":    "true",
		"egress/HOSTS":                         "api.declared.example:443",
		"internal-transport/mesh-egress-range": "10.0.0.0/8",
	})
	require.ErrorIs(t, err, auth.ErrInvalidMeshEgress)
	require.False(t, admitsDeclaredMeshAddress())

	require.Error(t, configureMeshEgress(meshEgressSource{"internal-transport/mesh-protected": "yes"}))

	// Nothing declared: nothing installed.
	require.NoError(t, configureMeshEgress(meshEgressSource{}))
	require.False(t, admitsDeclaredMeshAddress())
}

// The refusal is reached through the function main calls.
func TestStartupRefusesAMeshEgressRangeWithoutTheMesh(t *testing.T) {
	withdrawMeshEgress(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "false")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__EGRESS__HOSTS", "api.declared.example:443")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_EGRESS_RANGE", "240.240.0.0/16")
	err := requireStartupConfiguration(t.Context(), false)
	require.ErrorIs(t, err, auth.ErrMeshEgressWithoutMesh)
}

// The hosts are read from the variable Codefly delivers egress/HOSTS in:
// inert on a deployment that does not assert its mesh, admitted on one that
// does.
func TestMeshEgressReadsTheDeliveredEgressHosts(t *testing.T) {
	withdrawMeshEgress(t)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__EGRESS__HOSTS", "api.declared.example:443")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "false")
	require.NoError(t, configureMeshEgress(codefly.For(t.Context())))
	require.False(t, admitsDeclaredMeshAddress())

	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL_TRANSPORT__MESH_PROTECTED", "true")
	require.NoError(t, configureMeshEgress(codefly.For(t.Context())))
	require.True(t, admitsDeclaredMeshAddress())
}
