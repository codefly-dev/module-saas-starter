//go:build !pure

package redisintegration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	cache "github.com/codefly-dev/interface-cache/go/cache"
	"github.com/codefly-dev/interface-cache/go/cache/cachetest"
	rediscache "github.com/codefly-dev/service-redis/cache"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"accounts/pkg/infra"
)

// The codefly.dev/cache conformance suite against the shared layer the
// membership cache runs on: the service-redis driver, over a client opened by
// infra.NewRedisClient, on the server the pinned codefly.dev/redis agent runs.
// Run holds the layer to what it declares (Leases, Notifies within its
// NoticeBound, Resyncs, Fences); RunStack holds stacks over it — each with its
// own Memory tier, as each replica has — to every mode the declaration
// supports, FillLease and the default TTLBounded among them, every named
// pitfall, and the simulation's fixed seeds.
func TestMembershipCacheLayerConformsToTheCacheInterface(t *testing.T) {
	h := membershipCacheHarness(t)

	// The layer the service builds is the one the harness builds, but for its
	// fixed key prefix, which the suite replaces with a namespace per test.
	client := cacheClient(t)
	require.IsType(t, &rediscache.Layer{}, infra.MembershipCacheLayer(client))
	require.Equal(t,
		cache.CapabilitiesOf(infra.MembershipCacheLayer(client)),
		cache.CapabilitiesOf(h.New(t, cachetest.Namespace(t))))

	t.Run("Conformance", func(t *testing.T) { cachetest.Run(t, h) })
	t.Run("Stack", func(t *testing.T) { cachetest.RunStack(t, h) })
}

// membershipCacheHarness gives each namespace its own client, as separate
// replicas would have, and reaches the server directly for the writes and the
// connection drops a driver cannot make itself.
func membershipCacheHarness(t *testing.T) cachetest.Harness {
	server := cacheClient(t)
	return cachetest.Harness{
		New: func(t *testing.T, namespace string) cache.Layer {
			return rediscache.New(cacheClient(t), rediscache.WithPrefix(namespace))
		},
		// A plain SET of the driver's value key, as another program would.
		ExternalWrite: func(t *testing.T, namespace, key string) {
			require.NoError(t, server.Set(context.Background(), namespace+":v:{"+key+"}", "written externally", time.Minute).Err())
		},
		// The server drops the layer's tracking connection, as a network
		// failure or a restart would.
		Interrupt: func(t *testing.T, l cache.Layer) {
			name := l.(*rediscache.Layer).TrackingClientName()
			require.NotZero(t, killConnectionsNamed(t, server, name), "no tracking connection %q to interrupt", name)
		},
	}
}

func cacheClient(t *testing.T) *goredis.Client {
	t.Helper()
	client, err := infra.NewRedisClient(testCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// killConnectionsNamed CLIENT KILLs every connection named name and returns how
// many there were.
func killConnectionsNamed(t *testing.T, server *goredis.Client, name string) int {
	t.Helper()
	ctx := context.Background()
	list, err := server.ClientList(ctx).Result()
	require.NoError(t, err)
	killed := 0
	for _, line := range strings.Split(list, "\n") {
		fields := map[string]string{}
		for _, field := range strings.Fields(line) {
			if k, v, ok := strings.Cut(field, "="); ok {
				fields[k] = v
			}
		}
		if fields["name"] != name {
			continue
		}
		require.NoError(t, server.ClientKillByFilter(ctx, "ID", fields["id"]).Err())
		killed++
	}
	return killed
}
