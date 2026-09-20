//go:build !pure

package infra_test

import (
	"context"
	"testing"
	"time"

	"accounts/pkg/business"
	eventsv1 "accounts/pkg/gen/saas/events/v1"
	"accounts/pkg/infra"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// publishJournalEntry writes one committed event through the real publish path:
// inside the producing tenant's own transaction, which is both what the
// transactional-outbox rule asks of a producer and what publish_domain_event
// enforces — as app_tenant it refuses an event whose tenant is not the
// transaction's signed scope.
func publishJournalEntry(t *testing.T, transport *infra.PostgresEventTransport, tenantID, eventType, subject string) string {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	require.NoError(t, testStore.WithOrgTx(testCtx, tenantID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithOrgTx
		return transport.Publish(ctx, tx, &eventsv1.EventEnvelope{
			Id:              id,
			Type:            eventType,
			Source:          "urn:codefly:test/journal",
			Subject:         subject,
			Time:            timestamppb.New(time.Now().UTC()),
			Specversion:     "1.0",
			Datacontenttype: "application/json",
			Data:            []byte(`{"v":1}`),
			TenantId:        tenantID,
		})
	}))
	return id
}

func journalTransport(t *testing.T) *infra.PostgresEventTransport {
	t.Helper()
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return infra.NewPostgresEventTransport(infra.NewPostgresJobStore(pool), pool,
		"journal-"+uuid.NewString(), time.Minute)
}

func readTenantJournal(t *testing.T, orgID string, afterSeq int64, limit int) []business.JournalEntry {
	t.Helper()
	var page []business.JournalEntry
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		page, err = testStore.ListTenantJournal(ctx, afterSeq, limit)
		return err
	}))
	return page
}

func resolveTenantCursor(t *testing.T, orgID, eventID string) int64 {
	t.Helper()
	var cursor int64
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		cursor, err = testStore.ResolveTenantJournalCursor(ctx, eventID)
		return err
	}))
	return cursor
}

// The journal read carries no org predicate of its own: the tenant RLS policy on
// domain_events is what confines it, so this asserts against the real policy
// rather than against a WHERE clause a test could not distinguish it from.
func TestTenantJournalReadIsConfinedToTheCallersTenant(t *testing.T) {
	transport := journalTransport(t)
	mine := uuid.Must(uuid.NewV7()).String()
	theirs := uuid.Must(uuid.NewV7()).String()

	first := publishJournalEntry(t, transport, mine, "documents.entry.version_minted", "entry-1")
	foreign := publishJournalEntry(t, transport, theirs, "documents.entry.version_minted", "entry-2")
	second := publishJournalEntry(t, transport, mine, "documents.entry.renamed", "entry-1")

	page := readTenantJournal(t, mine, 0, 100)
	seen := map[string]business.JournalEntry{}
	for _, entry := range page {
		seen[entry.EventID] = entry
	}
	require.Contains(t, seen, first)
	require.Contains(t, seen, second)
	require.NotContains(t, seen, foreign)

	require.Equal(t, "documents.entry.version_minted", seen[first].Type)
	require.Equal(t, "entry-1", seen[first].Subject)
	require.Equal(t, "application/json", seen[first].DataContentType)
	require.JSONEq(t, `{"v":1}`, string(seen[first].Data))
	require.False(t, seen[first].EventTime.IsZero())
	require.Less(t, seen[first].Seq, seen[second].Seq)

	// The page is a keyset window: strictly after the cursor, in seq order, at
	// most limit rows.
	after := readTenantJournal(t, mine, seen[first].Seq, 100)
	for _, entry := range after {
		require.Greater(t, entry.Seq, seen[first].Seq)
	}
	require.Len(t, readTenantJournal(t, mine, 0, 1), 1)
}

func TestTenantJournalCursorCannotProbeAnotherTenant(t *testing.T) {
	transport := journalTransport(t)
	mine := uuid.Must(uuid.NewV7()).String()
	theirs := uuid.Must(uuid.NewV7()).String()

	own := publishJournalEntry(t, transport, mine, "documents.entry.version_minted", "entry-1")
	foreign := publishJournalEntry(t, transport, theirs, "documents.entry.version_minted", "entry-2")

	ownSeq := resolveTenantCursor(t, mine, own)
	require.Positive(t, ownSeq)

	head := resolveTenantCursor(t, mine, "")
	require.Equal(t, ownSeq, head, "the tenant's own last entry is its head")

	// A foreign id, an id that never existed and a cursor that is not a UUID must
	// all be the same answer, or the cursor becomes an existence oracle.
	for _, presented := range []string{foreign, uuid.NewString(), "not-a-uuid"} {
		require.Equal(t, head, resolveTenantCursor(t, mine, presented))
	}
}
