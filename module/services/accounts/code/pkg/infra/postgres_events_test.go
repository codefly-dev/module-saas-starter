package infra_test

import (
	"testing"
	"time"

	"accounts/pkg/events"
	"accounts/pkg/events/eventstest"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPostgresEventTransportConformance(t *testing.T) {
	const lease = 300 * time.Millisecond
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	eventstest.RunConformance(t, eventstest.Harness{
		New: func(_ *testing.T, subscriptions []events.Subscription) events.Transport {
			return infra.NewPostgresEventTransport(store, pool, subscriptions, "events-conformance-"+uuid.NewString(), lease)
		},
		LeaseDuration: lease,
	})
}
