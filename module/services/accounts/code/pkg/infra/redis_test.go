package infra_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// NewRedisClient fails boot when the cache connection is missing, on the
// premise that accounts always declares the dependency. Dropping it from the
// manifest would make every boot fail with a configuration error that no
// longer describes the cause, so the premise is pinned here.
func TestAccountsDeclaresTheCacheDependencyItRequires(t *testing.T) {
	data, err := os.ReadFile("../../../service.codefly.yaml")
	require.NoError(t, err)
	var manifest struct {
		ServiceDependencies []struct {
			Name string `yaml:"name"`
		} `yaml:"service-dependencies"`
	}
	require.NoError(t, yaml.Unmarshal(data, &manifest))
	var names []string
	for _, dependency := range manifest.ServiceDependencies {
		names = append(names, dependency.Name)
	}
	require.Contains(t, names, "cache")
}
