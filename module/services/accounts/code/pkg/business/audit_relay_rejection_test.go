package business

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// The relay sets a row aside only on what the store reports about that row: a
// *PermanentRowRejection naming its event. Everything else — however often it
// repeats, whatever else the store accepts in between — is a retryable failure
// that keeps the batch queued. These tests pin that rule from both sides: a
// store that is merely unreliable quarantines nothing, and a store that refuses
// rows quarantines exactly those rows however many there are and however small
// the batch.

// refusesForGood makes a store write fail while the batch holds any of ids,
// reporting every such row at once, as a warehouse that lists each invalid row
// of a request does.
func refusesForGood(ids ...string) func(AuditBatch) error {
	return func(batch AuditBatch) error {
		var refused []error
		for _, record := range batch.Records {
			if slices.Contains(ids, record.Entry.ID) {
				refused = append(refused, &PermanentRowRejection{
					EventID: record.Entry.ID,
					Cause:   fmt.Errorf("value of row %s is invalid", record.Entry.ID),
				})
			}
		}
		return errors.Join(refused...)
	}
}

// refusesOneAtATime is refusesForGood for a store that reports only the first
// refused row of a write.
func refusesOneAtATime(ids ...string) func(AuditBatch) error {
	all := refusesForGood(ids...)
	return func(batch AuditBatch) error {
		err := all(batch)
		if err == nil {
			return nil
		}
		return PermanentRowRejections(err)[0]
	}
}

// flaky makes a share of the writes that reach inner fail with an error that
// says nothing of any row, as a throttled or half-dead warehouse does. The
// failure does not look at the batch, so it never tells the relay about a row.
func flaky(seed int64, share float64, inner func(AuditBatch) error) func(AuditBatch) error {
	random := rand.New(rand.NewSource(seed))
	return func(batch AuditBatch) error {
		if random.Float64() < share {
			return errors.New("googleapi: Error 429: rateLimitExceeded")
		}
		if inner == nil {
			return nil
		}
		return inner(batch)
	}
}

// drainUntilEmpty runs passes, as the relay loop does, until the queue is empty.
func drainUntilEmpty(t *testing.T, f *relayFixture, limit int) (passes int) {
	t.Helper()
	for passes < limit {
		if len(f.queue.remaining()) == 0 {
			return passes
		}
		passes++
		_, _ = f.relay.DrainOnce(context.Background())
	}
	require.Fail(t, "the relay did not empty the queue", "%d rows remain after %d passes", len(f.queue.remaining()), passes)
	return passes
}

func TestAuditRelayQuarantinesNothingWhileTheStoreIsMerelyUnreliable(t *testing.T) {
	for _, seed := range []int64{1, 2, 3} {
		f := newRelayFixture(t, 20, time.Second, nil)
		ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 6000)...)
		f.warehouse.fail = flaky(seed, 0.5, nil)

		drainUntilEmpty(t, f, 100000)

		require.Empty(t, f.queue.quarantined(), "seed %d: a write that fails says nothing about the rows it held", seed)
		require.Len(t, f.warehouse.byID, len(ids), "seed %d: every row is delivered", seed)
	}
}

func TestAuditRelayRetriesEveryFailureThatIsNotARowRefusal(t *testing.T) {
	for name, failure := range map[string]error{
		"transport":     errors.New("Post \"https://bigquery.googleapis.com/…\": connection reset by peer"),
		"server":        errors.New("googleapi: Error 503: backendError"),
		"throttled":     errors.New("googleapi: Error 429: rateLimitExceeded"),
		"quota":         errors.New("googleapi: Error 403: quotaExceeded"),
		"timeout":       context.DeadlineExceeded,
		"quorum":        errors.New("code: 285, message: Number of alive replicas is less than requested quorum"),
		"unclassified":  errors.New("something the store did not explain"),
		"says invalid":  errors.New("row 3 rejected: invalid value"),
		"empty refusal": &PermanentRowRejection{},
	} {
		for _, size := range []int{1, 8} {
			f := newRelayFixture(t, size, time.Second, nil)
			f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, size)...)
			f.warehouse.fail = func(AuditBatch) error { return failure }

			delivered, err := f.relay.DrainOnce(context.Background())

			require.Error(t, err, "%s, batch of %d", name, size)
			require.Zero(t, delivered)
			require.Empty(t, f.queue.quarantined(), "%s, batch of %d: only a refusal that names a row sets it aside", name, size)
			require.Len(t, f.queue.remaining(), size, "%s, batch of %d: every row stays queued", name, size)
		}
	}
}

func TestAuditRelayDeliversEveryGoodRowAmongDenseRefusedRows(t *testing.T) {
	const rows = 500
	badEvery := map[string]int{"20 of 500": 25, "15 of 500": 33, "50 of 500": 10}
	stores := map[string]func(bad ...string) func(AuditBatch) error{
		"all reported at once": refusesForGood,
		"one reported a time":  refusesOneAtATime,
		"with a flaky store": func(bad ...string) func(AuditBatch) error {
			return flaky(7, 0.3, refusesForGood(bad...))
		},
	}
	for density, every := range badEvery {
		for storeName, store := range stores {
			f := newRelayFixture(t, rows, time.Second, nil)
			ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, rows)...)
			var bad, good []string
			for i, id := range ids {
				if i%every == 0 {
					bad = append(bad, id)
				} else {
					good = append(good, id)
				}
			}
			f.warehouse.fail = store(bad...)
			label := density + ", " + storeName

			drainUntilEmpty(t, f, 1000)

			require.ElementsMatch(t, bad, queuedIDs(f.queue.quarantined()), label+": exactly the refused rows are set aside")
			require.Len(t, f.warehouse.byID, len(good), label+": every other row is delivered")
			for _, id := range bad {
				require.NotContains(t, f.warehouse.byID, id, label)
				reason := f.queue.reasonOf(id)
				require.Contains(t, reason, "value of row "+id+" is invalid", label+": each keeps the store's own reason")
				require.True(t, strings.HasPrefix(reason, QuarantineWarehouseRejectedArchived+": "), label+": and says the archive holds it")
			}
			require.Len(t, f.archive.order, 1, label+": the archive holds the batch once, whole")
			require.ElementsMatch(t, ids, f.archive.eventIDs(), label)
		}
	}
}

func TestAuditRelayBatchOfOneDeliversTheRowsBehindARefusedFirstRow(t *testing.T) {
	f := newRelayFixture(t, 1, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 4)...)
	f.warehouse.fail = refusesForGood(ids[0])

	delivered, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err, "a refused row that is set aside is not a failed delivery")
	require.Equal(t, 3, delivered, "the rows behind the refused one are delivered")
	require.Equal(t, ids[:1], queuedIDs(f.queue.quarantined()))
	require.Empty(t, f.queue.remaining())
	require.ElementsMatch(t, ids[1:], mapKeys(f.warehouse.byID))
}

func TestAuditRelaySetsAsideALoneRefusedRowWithoutWaitingForAnotherEvent(t *testing.T) {
	f := newRelayFixture(t, 10, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, teeOrgID)
	f.warehouse.fail = refusesForGood(ids[0])

	_, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, ids, queuedIDs(f.queue.quarantined()))
	require.Empty(t, f.queue.remaining())
}

func TestAuditRelayGivesEveryUnserializableRowItsOwnReason(t *testing.T) {
	f := newRelayFixture(t, 5, time.Second, nil)
	ids := f.enqueue(time.Minute, EventSessionRevoked, repeatOrg(teeOrgID, 5)...)
	for _, i := range []int{0, 2, 3} {
		f.queue.rows[i].Entry.Payload = unencodable()
	}

	delivered, err := f.relay.DrainOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, delivered)
	require.ElementsMatch(t, []string{ids[0], ids[2], ids[3]}, queuedIDs(f.queue.quarantined()))
	require.ElementsMatch(t, []string{ids[1], ids[4]}, f.archive.eventIDs(), "a row that cannot be serialized cannot be archived: the quarantine holds its only copy")
	for _, i := range []int{0, 2, 3} {
		reason := f.queue.reasonOf(ids[i])
		require.True(t, strings.HasPrefix(reason, QuarantineUnserializableNotArchived+": "), "the reason says the row was never archived")
		require.Contains(t, reason, ids[i], "the reason names the row's own event")
		for _, j := range []int{0, 2, 3} {
			if j != i {
				require.NotContains(t, reason, ids[j], "and no other row's")
			}
		}
	}
}

func mapKeys(byID map[string]AuditRecord) []string {
	keys := make([]string, 0, len(byID))
	for id := range byID {
		keys = append(keys, id)
	}
	return keys
}

func TestAuditQuarantineReasonIsBoundedValidTextWithoutNul(t *testing.T) {
	require.Equal(t, "", AuditQuarantineReason(""))
	require.Equal(t, "plain error", AuditQuarantineReason("plain error"))
	exactly := strings.Repeat("a", 1000)
	require.Equal(t, exactly, AuditQuarantineReason(exactly), "text at the bound is kept whole")

	// A cut at byte 1000 must land between characters, whatever width they have
	// and wherever they start.
	for _, glyph := range []string{"é", "€", "😀"} {
		for lead := 0; lead < 4; lead++ {
			text := strings.Repeat("a", lead) + strings.Repeat(glyph, 600)
			got := AuditQuarantineReason(text)
			require.True(t, utf8.ValidString(got), "%q glyph after %d bytes", glyph, lead)
			require.LessOrEqual(t, len(got), 1000)
			require.Greater(t, len(got), 1000-len(glyph), "the cut is no earlier than the last whole character")
			require.True(t, strings.HasPrefix(text, got), "what is kept is a prefix, not a rewrite")
		}
	}

	require.Equal(t, "before\uFFFDafter", AuditQuarantineReason("before\x00after"), "a NUL is replaced")
	require.Equal(t, "bad \uFFFD bytes", AuditQuarantineReason("bad \xff\xfe bytes"), "invalid UTF-8 is replaced")
	got := AuditQuarantineReason(strings.Repeat("\x00", 2000))
	require.True(t, utf8.ValidString(got))
	require.NotContains(t, got, "\x00")
	require.LessOrEqual(t, len(got), 1000, "replacement is counted before the bound, not after it")
}
