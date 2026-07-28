package infra_test

import (
	"context"
	"errors"
	"testing"

	"accounts/pkg/analytics"
	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
)

func TestAnalyticsOutboxCommitsAndRollsBackWithDomainTransaction(t *testing.T) {
	userID := seedUser(t)
	orgID := seedOrg(t, userID)
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	outbox, err := analytics.NewOutbox(testStore, registry)
	require.NoError(t, err)
	newEvent := func(fact string) *analyticsv1.ProductEvent {
		event, eventErr := registry.NewEvent(analytics.NewEventInput{
			EventID:        analytics.DeterministicEventID("invite_created", fact),
			Name:           "invite_created",
			ActorUserID:    userID,
			OrganizationID: orgID,
			Source:         analyticsv1.EventSource_EVENT_SOURCE_API,
			Properties:     map[string]any{"role": "member"},
		})
		require.NoError(t, eventErr)
		return event
	}

	rollback := errors.New("roll back domain outcome")
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, outbox.Capture(ctx, newEvent("rolled-back")))
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return outbox.Capture(ctx, newEvent("committed"))
	}))

	_, jobStore := newJobExecutionHarness(t)
	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(registry, sink)
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: analytics.ExportQueue, Handler: handler,
	})
	require.NoError(t, err)
	processed, err := worker.RunOnce(testCtx)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.Len(t, sink.Events(), 1)
	require.Equal(
		t,
		analytics.DeterministicEventID("invite_created", "committed"),
		sink.Events()[0].GetEventId(),
	)
}
