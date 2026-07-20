package infra

import (
	"context"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/codefly-dev/core/wool"
)

const billingWorkerDatabaseRole = "app_billing_worker"

// NewBillingWorkerPool creates the dedicated cross-tenant pool used only for
// Stripe subscription projection. Generic receipt and lifecycle use the job
// worker pool. Request handlers keep using app_tenant; this role has BYPASSRLS
// but grants on only the small billing projection surface.
func NewBillingWorkerPool(ctx context.Context) (*pgxpool.Pool, error) {
	w := wool.Get(ctx).In("NewBillingWorkerPool")
	connection, err := codefly.For(ctx).Service("store").Secret("postgres", "read-write-connection")
	if err != nil {
		return nil, w.Wrapf(err, "failed to get connection string")
	}
	return NewBillingWorkerPoolFromURL(ctx, connection)
}

// NewBillingWorkerPoolFromURL is the explicit-URL variant used by integration
// tests. Every checkout reasserts the worker role; every release resets it so
// role state cannot leak between logical borrowers.
func NewBillingWorkerPoolFromURL(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	return newWorkerPoolFromURL(ctx, connectionURL, workerPoolConfig{
		role:            billingWorkerDatabaseRole,
		applicationName: "accounts-billing-worker",
		logScope:        "NewBillingWorkerPool",
	})
}
