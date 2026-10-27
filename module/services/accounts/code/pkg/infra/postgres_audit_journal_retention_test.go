//go:build !pure

package infra_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Each matrix runs in an owner transaction that rolls back. Clearing existing
// rows only inside that transaction gives the global old-unproven count an
// exact expectation without changing another test's committed journal.
func auditRetentionFixture(t *testing.T, fn func(context.Context, pgx.Tx, time.Time)) {
	t.Helper()
	asRetention(t, func(ctx context.Context, tx pgx.Tx) {
		_, err := tx.Exec(ctx, `DELETE FROM public.codefly_effect_receipts; DELETE FROM public.domain_events`)
		require.NoError(t, err)
		var cutoff time.Time
		require.NoError(t, tx.QueryRow(ctx, `SELECT clock_timestamp() - interval '720 hours'`).Scan(&cutoff))
		fn(ctx, tx, cutoff)
	})
}

func retentionJournalRow(t *testing.T, ctx context.Context, tx pgx.Tx, xact *string, eventType string, created, occurred time.Time, tenant *string) (string, string) {
	t.Helper()
	id := uuid.NewString()
	var seq int64
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO public.domain_events
		(id, type, source, request_fingerprint, xact_id, created_at, event_time, tenant_id)
		VALUES ($1, $2, 'saas/test', decode(repeat('ab',32),'hex'), $3::text::xid8, $4, $5, $6::text::uuid)
		RETURNING seq`, id, eventType, xact, created, occurred, tenant).Scan(&seq))
	position := business.JournalPosition{Seq: seq}
	if xact != nil {
		var err error
		position.Xact, err = strconv.ParseUint(*xact, 10, 64)
		require.NoError(t, err)
	}
	return id, position.String()
}

func retentionReceipt(t *testing.T, ctx context.Context, tx pgx.Tx, method, from, to string, change func(*gen.DeliverAuditEventsResponse, *[]byte)) {
	t.Helper()
	digest, err := receipts.RequestDigest(&gen.DeliverAuditEventsRequest{From: from, To: to})
	require.NoError(t, err)
	response := &gen.DeliverAuditEventsResponse{
		EffectId: business.AuditDeliveryEffectID(from), Status: "committed", Delivered: 1, From: from, To: to,
	}
	if change != nil {
		change(response, &digest)
	}
	wire, err := proto.Marshal(response)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO public.codefly_effect_receipts
		(tenant,effect_id,method,request_digest,response,committed_at)
		VALUES ($1,$2,$3,$4,$5,clock_timestamp()-interval '960 hours')`,
		uuid.NewString(), business.AuditDeliveryEffectID(from), method, digest, wire)
	require.NoError(t, err)
}

func TestAuditJournalRetentionPreservesHolesAndUnprovenEntries(t *testing.T) {
	auditRetentionFixture(t, func(ctx context.Context, tx pgx.Tx, cutoff time.Time) {
		old := cutoff.Add(-24 * time.Hour)
		tenant := uuid.NewString()
		var ids, positions []string
		for i := 0; i < 7; i++ {
			rowXact := strconv.Itoa(10 + i)
			var org *string // Both organization and no-organization records.
			if i%2 == 0 {
				org = &tenant
			}
			id, position := retentionJournalRow(t, ctx, tx, &rowXact, business.AuditRecordedEventType, old, time.Now(), org)
			ids, positions = append(ids, id), append(positions, position)
		}
		// The committed intervals overlap, but neither proves rows 0, 3 or 6.
		for _, interval := range [][2]int{{0, 2}, {1, 2}, {3, 5}} {
			retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, positions[interval[0]], positions[interval[1]], nil)
		}
		retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, positions[5], positions[6], func(r *gen.DeliverAuditEventsResponse, _ *[]byte) {
			r.From, r.To, r.Delivered = "", "", 500 // Legacy count cannot prove coverage.
		})
		// A young entry with an old producer timestamp, and the exact cutoff,
		// survive even though a committed interval covers their positions.
		xact := "20"
		young, youngPos := retentionJournalRow(t, ctx, tx, &xact, business.AuditRecordedEventType, cutoff.Add(time.Hour), old, nil)
		boundary, boundaryPos := retentionJournalRow(t, ctx, tx, &xact, business.AuditRecordedEventType, cutoff, old, nil)
		retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, positions[6], boundaryPos, nil)
		other, otherPos := retentionJournalRow(t, ctx, tx, &xact, "example.recorded.v1", old, old, nil)
		retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, youngPos, otherPos, nil)
		_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		removed, unproven, err := testStore.DeleteDeliveredAuditJournal(infra.BindControlPlaneTx(ctx, tx), cutoff)
		require.NoError(t, err)
		require.EqualValues(t, 4, removed)
		require.EqualValues(t, 3, unproven)
		rows, err := tx.Query(ctx, `SELECT id::text FROM public.domain_events ORDER BY seq`)
		require.NoError(t, err)
		remaining, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		require.Equal(t, []string{ids[0], ids[3], ids[6], young, boundary, other}, remaining)
	})
}

func TestAuditJournalRetentionRequiresConsistentCommittedCoverage(t *testing.T) {
	cases := map[string]func(*gen.DeliverAuditEventsResponse, *[]byte){
		"legacy":            func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.From, r.To = "", "" },
		"uncommitted":       func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.Status = "pending" },
		"different effect":  func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.EffectId = "different" },
		"different request": func(_ *gen.DeliverAuditEventsResponse, d *[]byte) { *d = []byte("wrong digest") },
		"invalid position":  func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.To = "v1.01.2" },
		"reversed interval": func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.From, r.To = r.To, r.From },
		"empty interval":    func(r *gen.DeliverAuditEventsResponse, _ *[]byte) { r.To = r.From },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			auditRetentionFixture(t, func(ctx context.Context, tx pgx.Tx, cutoff time.Time) {
				_, position := retentionJournalRow(t, ctx, tx, nil, business.AuditRecordedEventType, cutoff.Add(-time.Hour), time.Now(), nil)
				retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, business.JournalStart, position, change)
				_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
				require.NoError(t, err)
				removed, unproven, err := testStore.DeleteDeliveredAuditJournal(infra.BindControlPlaneTx(ctx, tx), cutoff)
				require.NoError(t, err)
				require.Zero(t, removed)
				require.EqualValues(t, 1, unproven)
			})
		})
	}
	t.Run("malformed protobuf", func(t *testing.T) {
		auditRetentionFixture(t, func(ctx context.Context, tx pgx.Tx, cutoff time.Time) {
			_, position := retentionJournalRow(t, ctx, tx, nil, business.AuditRecordedEventType, cutoff.Add(-time.Hour), time.Now(), nil)
			retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, business.JournalStart, position, nil)
			_, err := tx.Exec(ctx, `UPDATE public.codefly_effect_receipts SET response = decode('ff','hex'); SET LOCAL ROLE app_control_plane`)
			require.NoError(t, err)
			removed, unproven, err := testStore.DeleteDeliveredAuditJournal(infra.BindControlPlaneTx(ctx, tx), cutoff)
			require.NoError(t, err)
			require.Zero(t, removed)
			require.EqualValues(t, 1, unproven)
		})
	})
}

func TestAuditJournalRetentionRoleCannotDeleteOtherTypesOrYoungRecords(t *testing.T) {
	auditRetentionFixture(t, func(ctx context.Context, tx pgx.Tx, cutoff time.Time) {
		old := cutoff.Add(-time.Hour)
		_, oldPos := retentionJournalRow(t, ctx, tx, nil, business.AuditRecordedEventType, old, time.Now(), nil)
		_, youngPos := retentionJournalRow(t, ctx, tx, nil, business.AuditRecordedEventType, cutoff.Add(time.Hour), old, nil)
		_, _ = retentionJournalRow(t, ctx, tx, nil, "example.recorded.v1", old, old, nil)
		retentionReceipt(t, ctx, tx, business.DeliverAuditEventsMethod, business.JournalStart, youngPos, nil)
		retentionReceipt(t, ctx, tx, business.SourceOperationMethod, business.JournalStart, oldPos, nil)
		_, err := tx.Exec(ctx, `SET LOCAL ROLE app_control_plane`)
		require.NoError(t, err)
		var methods []string
		rows, err := tx.Query(ctx, `SELECT method FROM public.codefly_effect_receipts`)
		require.NoError(t, err)
		methods, err = pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		require.Equal(t, []string{business.DeliverAuditEventsMethod}, methods, "ordinary source response bytes remain hidden")
		tag, err := tx.Exec(ctx, `DELETE FROM public.domain_events WHERE type <> $1 OR created_at >= $2`, business.AuditRecordedEventType, cutoff)
		require.NoError(t, err)
		require.Zero(t, tag.RowsAffected(), "RLS blocks direct deletion of other types and young audit records")
		removed, unproven, err := testStore.DeleteDeliveredAuditJournal(infra.BindControlPlaneTx(ctx, tx), cutoff)
		require.NoError(t, err)
		require.EqualValues(t, 1, removed, "a committed range can retire pre-xact_id rows from JournalStart")
		require.Zero(t, unproven)
	})
}

func TestAuditJournalRetentionRefusesRequestOrUnboundTransactions(t *testing.T) {
	_, _, err := testStore.DeleteDeliveredAuditJournal(testCtx, time.Now())
	require.ErrorContains(t, err, "control-plane transaction")
	org := seedOrg(t, seedUser(t))
	require.NoError(t, testStore.WithOrgTx(testCtx, org, func(ctx context.Context) error {
		_, _, err := testStore.DeleteDeliveredAuditJournal(ctx, time.Now())
		require.ErrorContains(t, err, "control-plane transaction")
		return nil
	}))
}
