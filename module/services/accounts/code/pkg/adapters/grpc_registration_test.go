package adapters

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestCatalogGRPCRegistrationMatchesGeneratedNativeSubset(t *testing.T) {
	server, err := NewGrpServer(&Configuration{})
	require.NoError(t, err)

	for _, listener := range []*grpc.Server{server.gRPC, server.internalGRPC} {
		var registered []string
		for name := range listener.GetServiceInfo() {
			if strings.HasPrefix(name, "saas.accounts.v1.") {
				registered = append(registered, name)
			}
		}
		sort.Strings(registered)
		require.Equal(t, catalogGRPCServiceNames, registered)
		for _, name := range catalogConnectOnlyServiceNames {
			require.NotContains(t, listener.GetServiceInfo(), name)
		}
	}

	require.Len(t, catalogGRPCServiceNames, 15)
	require.Len(t, catalogConnectOnlyServiceNames, 9)
}
