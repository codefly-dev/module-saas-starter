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
	workerPool, jobStore := newJobExecutionHarness(t)
	rolledBackFact := "rolled-back-" + orgID
	committedFact := "committed-" + orgID
	rolledBackEventID := analytics.DeterministicEventID("invite_created", rolledBackFact)
	committedEventID := analytics.DeterministicEventID("invite_created", committedFact)
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
		require.NoError(t, outbox.Capture(ctx, newEvent(rolledBackFact)))
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return outbox.Capture(ctx, newEvent(committedFact))
	}))

	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  jobStore,
	})
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: analytics.ExportQueue, Handler: handler,
	})
	require.NoError(t, err)
	// The named test-infra database survives across package invocations. Drain
	// earlier analytics work until this invocation's unique event arrives rather
	// than assuming the shared queue starts empty.
	foundCommitted := false
	for attempts := 0; attempts < 1000 && !foundCommitted; attempts++ {
		processed, runErr := worker.RunOnce(testCtx)
		require.NoError(t, runErr)
		if processed == 0 {
			break
		}
		for _, event := range sink.Events() {
			foundCommitted = foundCommitted || event.GetEventId() == committedEventID
		}
	}
	require.True(t, foundCommitted, "worker must deliver this invocation's committed event")
	for _, event := range sink.Events() {
		require.NotEqual(t, rolledBackEventID, event.GetEventId(), "rolled-back event must never reach the queue")
	}
	var commandID, kind, providerReference string
	require.NoError(t, workerPool.QueryRow(testCtx, `
		SELECT command_id::text, kind, provider_reference
		FROM analytics_deliveries
		WHERE command_id = $1`,
		committedEventID,
	).Scan(&commandID, &kind, &providerReference))
	require.Equal(t, committedEventID, commandID)
	require.Equal(t, "event", kind)
	require.Equal(t, commandID, providerReference)
}
