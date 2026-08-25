package adapters

import (
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// reflectionServiceNames are the service names gRPC server reflection registers.
// If either is present in a server's ServiceInfo, reflection is exposed on it.
var reflectionServiceNames = []string{
	"grpc.reflection.v1.ServerReflection",
	"grpc.reflection.v1alpha.ServerReflection",
}

func reflectionRegistered(server *grpc.Server) bool {
	info := server.GetServiceInfo()
	for _, name := range reflectionServiceNames {
		if _, ok := info[name]; ok {
			return true
		}
	}
	return false
}

// TestReflectionRegisteredOnlyLocally pins the environment gate on gRPC server
// reflection. grpc_gen.go is generated, so a regeneration that emits an
// unconditional reflection.Register would re-expose the full service surface to
// any mesh-internal caller in production; this test fails if that gate is lost.
func TestReflectionRegisteredOnlyLocally(t *testing.T) {
	t.Run("local registers reflection", func(t *testing.T) {
		t.Setenv(resources.EnvironmentPrefix, resources.LocalEnvironment().Name)
		server, err := NewGrpServer(&Configuration{})
		require.NoError(t, err)
		require.True(t, reflectionRegistered(server.gRPC),
			"reflection must be registered locally to aid discovery")
		require.False(t, reflectionRegistered(server.internalGRPC),
			"reflection is never registered on the internal listener")
	})

	t.Run("deployed does not register reflection", func(t *testing.T) {
		t.Setenv(resources.EnvironmentPrefix, "production")
		server, err := NewGrpServer(&Configuration{})
		require.NoError(t, err)
		require.False(t, reflectionRegistered(server.gRPC),
			"reflection must not be registered outside local")
		require.False(t, reflectionRegistered(server.internalGRPC))
	})
}
