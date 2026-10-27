package cataloggen_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"gopkg.in/yaml.v3"

	_ "accounts/pkg/business" // links the interface descriptors
)

// The module declares the host interfaces on the endpoint that serves them: the
// one whose contract surface carries every method of every implemented
// interface service (the solution's InstallableService aside). Declared on an
// endpoint that does not serve them, a consumer bound to the interface would
// reach a listener that refuses each call.
func TestImplementedInterfacesAreDeclaredOnTheEndpointThatServesThem(t *testing.T) {
	var module struct {
		Interface struct {
			Endpoints []struct {
				Service    string   `yaml:"service"`
				Endpoint   string   `yaml:"endpoint"`
				Implements []string `yaml:"implements"`
			} `yaml:"endpoints"`
		} `yaml:"interface"`
	}
	require.NoError(t, yaml.Unmarshal(readFixture(t, "../../../../../module.codefly.yaml"), &module))
	var surfaces map[string][]struct {
		FullName   string   `json:"fullName"`
		Procedures []string `json:"procedures"`
	}
	require.NoError(t, json.Unmarshal(readFixture(t, "../../../generated/api-contract-surfaces.json"), &surfaces))
	served := func(endpoint string) map[string]bool {
		out := map[string]bool{}
		for _, service := range surfaces[endpoint] {
			for _, procedure := range service.Procedures {
				out[procedure] = true
			}
		}
		return out
	}

	declaring := 0
	for _, entry := range module.Interface.Endpoints {
		if len(entry.Implements) == 0 {
			continue
		}
		declaring++
		require.Equal(t, "accounts", entry.Service)
		procedures := served(entry.Endpoint)
		for _, id := range entry.Implements {
			name, _, ok := strings.Cut(strings.TrimPrefix(id, "codefly.dev/"), "@")
			require.True(t, ok, "interface id %q", id)
			// Each 0.x interface declares protobuf package host.<name>.v1 in its
			// interface.codefly.yaml.
			pkg := "host." + name + ".v1"
			methods := interfaceMethods(t, pkg)
			require.NotEmpty(t, methods, "%s: no service of %s is linked", id, pkg)
			for _, method := range methods {
				require.True(t, procedures[method], "%s declares %s on accounts/%s, which does not serve %s", id, pkg, entry.Endpoint, method)
			}
		}
	}
	require.Equal(t, 1, declaring, "exactly one endpoint declares the host interfaces")
	for procedure := range served("authority") {
		require.False(t, strings.HasPrefix(procedure, "/host."), "the module authority endpoint serves %s", procedure)
	}
}

// interfaceMethods is every method of the package's services this host
// implements: the installation interface's InstallableService is the solution's.
func interfaceMethods(t *testing.T, pkg string) []string {
	t.Helper()
	var methods []string
	protoregistry.GlobalFiles.RangeFilesByPackage(protoreflect.FullName(pkg), func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			service := file.Services().Get(i)
			// InstallableService is the solution's side of the installation
			// interface, and OfferService the local authority's setup read beside
			// it: codefly.dev/installation declares InstallationService alone.
			if service.FullName() == "host.installation.v1.InstallableService" || service.FullName() == "host.installation.v1.OfferService" {
				continue
			}
			for j := 0; j < service.Methods().Len(); j++ {
				methods = append(methods, fmt.Sprintf("/%s/%s", service.FullName(), service.Methods().Get(j).Name()))
			}
		}
		return true
	})
	return methods
}
