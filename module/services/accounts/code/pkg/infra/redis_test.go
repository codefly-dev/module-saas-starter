package infra_test

import (
	"context"
	"os"
	"testing"

	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"accounts/pkg/infra"
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

// cacheConnection makes the cache dependency's redis connection resolve to
// value through the SDK's own lookup ("" for none), and points the SDK's local
// fallback at an environment with no configuration files, so nothing on the
// machine running the test can supply one.
func cacheConnection(t *testing.T, value string) {
	t.Helper()
	t.Setenv(resources.ModulePrefix, "saas-starter")
	t.Setenv(resources.EnvironmentPrefix, "no-such-environment-"+t.Name())
	unique := resources.ServiceUnique("saas-starter", "cache")
	t.Setenv(resources.ServiceSecretConfigurationKeyFromUnique(unique, "redis", "connection"), value)
}

// A declared cache whose connection is missing or malformed is a configuration
// error: before #927 it booted accounts without Redis, which silently dropped
// token revocation, cross-replica nonces and rate limiting.
func TestNewRedisClientRefusesAnUnusableCacheConnection(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		cacheConnection(t, "")
		client, err := infra.NewRedisClient(context.Background())
		require.ErrorContains(t, err, "redis connection is unavailable")
		require.Nil(t, client)
	})
	t.Run("malformed", func(t *testing.T) {
		cacheConnection(t, "not a redis url")
		client, err := infra.NewRedisClient(context.Background())
		require.ErrorContains(t, err, "cannot parse")
		require.Nil(t, client)
	})
	t.Run("usable", func(t *testing.T) {
		// Parsed only: NewRedisClient does not contact the server.
		cacheConnection(t, "redis://cache.invalid:6379")
		client, err := infra.NewRedisClient(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })
		require.Equal(t, "cache.invalid:6379", client.Options().Addr)
	})
}
