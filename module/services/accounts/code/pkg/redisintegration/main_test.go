//go:build !pure

package redisintegration_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/codefly-dev/core/sdk"
	"github.com/codefly-dev/core/wool"
	codefly "github.com/codefly-dev/sdk-go"

	"accounts/internal/testdb"
	"accounts/pkg/business"
	"accounts/pkg/infra"
)

var (
	testStore *infra.PostgresStore
	testCtx   context.Context
)

// TestMain boots the real store and the real `cache` service (the
// codefly.dev/redis agent this module pins) through codefly, the way the other
// accounts database suites boot the store. Every Redis client in this package
// comes from infra.NewRedisClient, so it reads the connection the dependency
// resolves, exactly as work.go does.
func TestMain(m *testing.M) {
	exitCode, err := testdb.RunWithPackageLock(func() int {
		return runRedisIntegrationTests(m)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration test lifecycle lock: %v\n", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

func runRedisIntegrationTests(m *testing.M) int {
	ctx := context.Background()
	wool.SetGlobalLogLevel(wool.DEBUG)

	setupDone := testdb.MeasureSetup("redis-db", []string{"cache", "store"}, 120*time.Second)
	deps, err := sdk.WithDependencies(ctx,
		sdk.WithDebug(),
		sdk.WithSharedControlChannel(),
		sdk.WithExcludedDependencies("vault", "telemetry"),
		sdk.WithNamingScope("redis-integration"),
		sdk.WithTimeout(120*time.Second),
		sdk.WithSilence("store", "cache"),
	)
	setupDone(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WithDependencies: %v\n", err)
		return 1
	}
	defer func() { _ = deps.Destroy(ctx) }()

	if _, err := codefly.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "codefly.Init: %v\n", err)
		return 1
	}

	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStore: %v\n", err)
		return 1
	}
	defer store.Close()
	if err := store.WithControlPlane(ctx, func(ctx context.Context) error {
		return store.SyncAuditEventTypes(ctx, business.AuditEventCatalog())
	}); err != nil {
		fmt.Fprintf(os.Stderr, "SyncAuditEventTypes: %v\n", err)
		return 1
	}
	testStore, testCtx = store, ctx

	executionDone := testdb.Measure("redis-db", "test-execution", nil, 0)
	exitCode := m.Run()
	executionDone(exitCode != 0)
	return exitCode
}
