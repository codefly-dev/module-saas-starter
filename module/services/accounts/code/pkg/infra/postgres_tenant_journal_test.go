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
func publishEnvelope(transport *infra.PostgresEventTransport, ctx context.Context, tx pgx.Tx, id, tenantID, subject string) error {
	return transport.Publish(ctx, tx, &eventsv1.EventEnvelope{
		Id:              id,
		Type:            "documents.entry.version_minted",
		Source:          "urn:codefly:test/journal",
		Subject:         subject,
		Time:            timestamppb.New(time.Now().UTC()),
		Specversion:     "1.0",
		Datacontenttype: "application/json",
		Data:            []byte(`{"v":1}`),
		TenantId:        tenantID,
	})
}

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
		page, err = testStore.ListTenantJournal(ctx, orgID, afterSeq, limit)
		return err
	}))
	return page
}

func resolveTenantCursor(t *testing.T, orgID, eventID string) (int64, bool) {
	t.Helper()
	var cursor int64
	var resolved bool
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		cursor, resolved, err = testStore.ResolveTenantJournalCursor(ctx, orgID, eventID)
		return err
	}))
	return cursor, resolved
}

func loadTenantPayloads(t *testing.T, orgID string, ids []string, maxBytes int) map[string]business.JournalPayload {
	t.Helper()
	var payloads map[string]business.JournalPayload
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		var err error
		payloads, err = testStore.LoadTenantJournalPayloads(ctx, orgID, ids, maxBytes)
		return err
	}))
	return payloads
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

// The page carries identities only. Reading payloads before visibility is
// resolved would charge a reader with no access for every byte in the tenant,
// and domain_events.data has no size CHECK of its own to bound it.
func TestTenantJournalPayloadsAreSeparateAndSizeBounded(t *testing.T) {
	transport := journalTransport(t)
	mine := uuid.Must(uuid.NewV7()).String()
	theirs := uuid.Must(uuid.NewV7()).String()

	small := publishJournalEntry(t, transport, mine, "documents.entry.version_minted", "entry-1")
	foreign := publishJournalEntry(t, transport, theirs, "documents.entry.version_minted", "entry-2")

	payloads := loadTenantPayloads(t, mine, []string{small, foreign}, 1024)
	require.Contains(t, payloads, small)
	require.Equal(t, "application/json", payloads[small].ContentType)
	require.JSONEq(t, `{"v":1}`, string(payloads[small].Data))
	require.NotContains(t, payloads, foreign, "another tenant's payload must not be readable by id")

	// Over the byte bound the entry is ABSENT rather than truncated, so a caller
	// can tell "no payload" from "too large to stream".
	require.NotContains(t, loadTenantPayloads(t, mine, []string{small}, 1), small)
}

func TestTenantJournalCursorCannotProbeAnotherTenant(t *testing.T) {
	transport := journalTransport(t)
	mine := uuid.Must(uuid.NewV7()).String()
	theirs := uuid.Must(uuid.NewV7()).String()

	own := publishJournalEntry(t, transport, mine, "documents.entry.version_minted", "entry-1")
	foreign := publishJournalEntry(t, transport, theirs, "documents.entry.version_minted", "entry-2")

	ownSeq, ownResolved := resolveTenantCursor(t, mine, own)
	require.Positive(t, ownSeq)
	require.True(t, ownResolved)

	head, headResolved := resolveTenantCursor(t, mine, "")
	require.Equal(t, ownSeq, head, "the tenant's own last entry is its head")
	require.False(t, headResolved)

	// A foreign id, an id that never existed and a cursor that is not a UUID must
	// all be the same answer, or the cursor becomes an existence oracle. They are
	// reported unresolved so the reader can be told its history was skipped —
	// which discloses nothing, because "not in your tenant" is what the caller
	// already knows and says nothing about any other tenant.
	for _, presented := range []string{foreign, uuid.NewString(), "not-a-uuid"} {
		seq, resolved := resolveTenantCursor(t, mine, presented)
		require.Equal(t, head, seq)
		require.False(t, resolved)
	}
}

// seq is taken at INSERT (an identity column) and the row becomes visible at
// COMMIT, and a followable type declares no partition so publish_domain_event
// takes no serializing lock. A producer can therefore hold a LOW seq and commit
// after a higher one is already visible. A reader that trusted seq as a
// visibility frontier would advance past the low seq and never look back — the
// entry would sit in the journal with no cursor position that could ever yield
// it. The re-read window below the cursor is what makes that recoverable.
func TestTenantJournalReReadWindowRecoversALateCommit(t *testing.T) {
	transport := journalTransport(t)
	tenant := uuid.Must(uuid.NewV7()).String()

	published := make(chan struct{})
	release := make(chan struct{})
	slowDone := make(chan error, 1)
	slowID := uuid.Must(uuid.NewV7()).String()

	go func() {
		slowDone <- testStore.WithOrgTx(context.Background(), tenant, func(ctx context.Context) error {
			tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithOrgTx
			if err := publishEnvelope(transport, ctx, tx, slowID, tenant, "entry-slow"); err != nil {
				return err
			}
			close(published)
			<-release
			return nil
		})
	}()
	<-published

	// The fast producer takes a HIGHER seq and commits first.
	fastID := publishJournalEntry(t, transport, tenant, "documents.entry.version_minted", "entry-fast")

	visible := readTenantJournal(t, tenant, 0, 100)
	require.Len(t, visible, 1, "only the committed entry is visible")
	require.Equal(t, fastID, visible[0].EventID)
	cursor := visible[0].Seq

	close(release)
	require.NoError(t, <-slowDone)

	// A plain high-water mark finds nothing; the re-read window finds the entry.
	require.Empty(t, readTenantJournal(t, tenant, cursor, 100),
		"the late entry sits BELOW the cursor, which is exactly why a watermark loses it")

	from := cursor - business.JournalRescanDepth
	if from < 0 {
		from = 0
	}
	recovered := readTenantJournal(t, tenant, from, 100)
	ids := map[string]bool{}
	for _, entry := range recovered {
		ids[entry.EventID] = true
	}
	require.True(t, ids[slowID], "the re-read window must recover an entry whose transaction committed late")
}
