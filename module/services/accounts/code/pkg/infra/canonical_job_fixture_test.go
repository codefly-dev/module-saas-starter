//go:build !pure

package infra

import (
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"context"
)

// EnqueueCanonicalAssociationFixture uses the existing host setup producer to
// create immutable association fixtures. Generic module enqueue deliberately
// refuses pending solution authority and is not the host setup producer.
func EnqueueCanonicalAssociationFixture(ctx context.Context, store *PostgresStore, org string, request *jobsv1.EnqueueJobRequest) (response *jobsv1.EnqueueJobResponse, err error) {
	err = store.WithControlPlane(ctx, func(ctx context.Context) error {
		response, err = insertInstallationJob(ctx, store.getQueryExecutor(ctx), org, request)
		return err
	})
	return
}
