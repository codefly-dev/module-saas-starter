package infra

import (
	"context"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/codefly-dev/core/wool"
)

const jobWorkerDatabaseRole = "app_job_worker"

// NewJobWorkerPool creates the cross-tenant pool reserved for the common
// inbox/outbox platform. Product request handlers continue to use app_tenant;
// every migrated workload, starting with Stripe, executes lifecycle operations
// through this boundary.
func NewJobWorkerPool(ctx context.Context) (*pgxpool.Pool, error) {
	w := wool.Get(ctx).In("NewJobWorkerPool")
	connection, err := codefly.For(ctx).Service("store").Secret("postgres", "read-write-connection")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get connection string")
	}
	return NewJobWorkerPoolFromURL(ctx, connection)
}

// NewJobWorkerPoolFromURL is the explicit-URL variant for integration tests.
func NewJobWorkerPoolFromURL(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	return newWorkerPoolFromURL(ctx, connectionURL, workerPoolConfig{
		role:            jobWorkerDatabaseRole,
		applicationName: "accounts-job-worker",
		logScope:        "NewJobWorkerPool",
	})
}
