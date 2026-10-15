//go:build !pure

package infra_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra/storetx"

	"connectrpc.com/connect"
	"github.com/codefly-dev/sdk-go/receipts/connecttransport"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// dedupWarehouse is a warehouse that, like the real one, may hold an entry twice
// and returns each entry once, by id.
type dedupWarehouse struct{ appended []business.AuditEntry }

func (w *dedupWarehouse) Append(_ context.Context, entry business.AuditEntry) error {
	w.appended = append(w.appended, entry)
	return nil
}

func (w *dedupWarehouse) List(context.Context, business.AuditRead) ([]business.AuditEntry, string, error) {
	once := map[string]business.AuditEntry{}
	for _, entry := range w.appended {
		once[entry.ID] = entry
	}
	out := make([]business.AuditEntry, 0, len(once))
	for _, entry := range once {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, "", nil
}

func (w *dedupWarehouse) Aggregate(context.Context, business.AuditRead, business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	return nil, nil
}

// DeliverAuditEvents over the real journal and the real receipt store: a range is
// appended with each record's organization taken from its row, one receipt is
// written per range under the installation's tenant, a replay of the same range is
// answered from the receipt with no second append, and overlapping ranges append
// at least once while a read of the warehouse returns each entry once. Delivery
// writes no audit of its own.
//
// The generated operation marking and real SDK guard serialize and replay the range.
func TestDeliverAuditEventsWritesOneReceiptPerRangeAndReplaysFromIt(t *testing.T) {
	warehouse := &dedupWarehouse{}
	audit, err := business.NewAuditService(business.AuditServiceConfig{
		Backend: business.AuditBackendWarehouse, Store: testStore, Producer: testStore,
		Transport: auditJournalTransport(t), Warehouse: warehouse,
	})
	require.NoError(t, err)
	svc, caller := journalReader(t, true)
	svc.SetAuditService(audit)
	receiptStore, closeReceipts, err := testStore.NewEffectReceipts()
	require.NoError(t, err)
	defer closeReceipts()
	require.NoError(t, svc.ConfigureSourceOperationReceipts(receiptStore))

	installation := seedOrg(t, seedUser(t))
	orgA, orgB := seedOrg(t, seedUser(t)), seedOrg(t, seedUser(t))
	head := drainJournal(t, svc, caller, business.JournalStart, business.AuditRecordedEventType).next

	// Three audit records: one for an organization, one for none, one for another.
	var ids []string
	for _, org := range []string{orgA, "", orgB} {
		envelope := auditJournalEnvelopeOf(t, seedUser(t), org)
		require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
			return auditJournalTransport(t).Publish(ctx, storetx.Tx(ctx), envelope)
		}))
		ids = append(ids, envelope.GetId())
	}
	records := drainJournal(t, svc, caller, head, business.AuditRecordedEventType)
	require.Equal(t, ids, eventIDs(business.ModuleReadJournalResult{Entries: records.entries}))
	position := func(i int) string { return records.entries[i].Position }

	handlerRuns := 0
	_, guard := svc.SourceOperationReceipts()
	require.True(t, guard.IsOperation(business.DeliverAuditEventsMethod))
	handle := func(from, to string) (*gen.DeliverAuditEventsResponse, error) {
		effectID := business.AuditDeliveryEffectID(from)
		ctx := auth.WithVerifiedDatabaseIdentity(testCtx, caller.PrincipalID, installation)
		ctx = business.WithSourceOperationRecheck(ctx, func(context.Context) error { return nil })
		req := connect.NewRequest(&gen.DeliverAuditEventsRequest{From: from, To: to})
		req.Header().Set(receipts.EffectIDHeaderName, effectID)
		response, err := connecttransport.WrapUnary(guard, business.DeliverAuditEventsMethod,
			func(ctx context.Context, req *connect.Request[gen.DeliverAuditEventsRequest]) (*connect.Response[gen.DeliverAuditEventsResponse], error) {
				handlerRuns++
				var out *gen.DeliverAuditEventsResponse
				_, err := svc.DeliverAuditEvents(ctx, req.Msg.From, req.Msg.To, func(ctx context.Context, r business.AuditDeliveryResult) error {
					out = &gen.DeliverAuditEventsResponse{EffectId: effectID, Status: "committed", Delivered: int32(r.Delivered), From: req.Msg.From, To: req.Msg.To}
					return svc.RecordSourceOperationReceipt(ctx, out)
				})
				if err != nil {
					return nil, err
				}
				return connect.NewResponse(out), nil
			})(ctx, req)
		if err != nil {
			return nil, err
		}
		return response.Msg, nil
	}
	receiptCount := func(tenant, from string) int {
		_, found, err := receiptStore.Lookup(testCtx, tenant, business.AuditDeliveryEffectID(from), business.DeliverAuditEventsMethod)
		require.NoError(t, err)
		if found {
			return 1
		}
		return 0
	}
	entriesOf := func() map[string]string { // entry id -> organization appended
		out := map[string]string{}
		for _, entry := range warehouse.appended {
			out[business.AuditJournalEventID(entry.ID)] = entry.OrgID
		}
		return out
	}

	// The first range: all three records, from the start.
	first, err := handle(business.JournalStart, position(2))
	require.NoError(t, err)
	require.EqualValues(t, 3, first.Delivered)
	require.Equal(t, business.JournalStart, first.From)
	require.Equal(t, position(2), first.To)
	require.Len(t, warehouse.appended, 3)
	require.Equal(t, map[string]string{ids[0]: orgA, ids[1]: "", ids[2]: orgB}, entriesOf(),
		"each record's organization is its row's, and the one with none has none")
	require.Equal(t, 1, receiptCount(installation, business.JournalStart), "one receipt, under the installation's tenant")
	require.Zero(t, receiptCount(orgA, business.JournalStart), "no receipt is written under a record's organization")

	// A replay of the range is answered from the receipt: no second append.
	replay, err := handle(business.JournalStart, position(2))
	require.NoError(t, err)
	require.True(t, proto.Equal(first, replay))
	require.Equal(t, 1, handlerRuns)
	require.Len(t, warehouse.appended, 3)

	// The same start with another end is another request under one effect id.
	_, err = handle(business.JournalStart, position(1))
	require.ErrorIs(t, err, receipts.ErrEffectIDReused)

	// An overlapping range (records 2 and 3 again) appends them a second time under its own receipt, and a
	// read of the warehouse still returns each entry once.
	overlap, err := handle(position(0), position(2))
	require.NoError(t, err)
	require.EqualValues(t, 2, overlap.Delivered)
	require.Len(t, warehouse.appended, 5, "at least once")
	require.Equal(t, 1, receiptCount(installation, position(0)))
	listed, _, err := warehouse.List(testCtx, business.AuditRead{})
	require.NoError(t, err)
	require.Len(t, listed, 3, "every read returns each entry once")

	// An end that could still gain rows is refused and writes nothing.
	open := openControlPlaneTx(t)
	open.do(t, func(ctx context.Context) error {
		var xact string
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xact)
	})
	later := auditJournalEnvelopeOf(t, seedUser(t), orgA)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return auditJournalTransport(t).Publish(ctx, storetx.Tx(ctx), later)
	}))
	var laterXact, laterSeq string
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return storetx.Tx(ctx).QueryRow(ctx, `SELECT xact_id::text, seq::text FROM domain_events WHERE id = $1::uuid`, later.GetId()).Scan(&laterXact, &laterSeq)
	}))
	beyond := "v1." + laterXact + "." + laterSeq
	_, err = handle(position(2), beyond)
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "the writer with the earlier transaction id has not finished: %v", err)
	require.Equal(t, 0, receiptCount(installation, position(2)))
	require.Len(t, warehouse.appended, 5)
	open.commit(t)
	done, err := handle(position(2), beyond)
	require.NoError(t, err)
	require.EqualValues(t, 1, done.Delivered)

	// Delivering wrote no audit entry of its own: nothing after the last record
	// this test published, but that record.
	tail := drainJournal(t, svc, caller, position(2), business.AuditRecordedEventType)
	require.Equal(t, []string{later.GetId()}, eventIDs(business.ModuleReadJournalResult{Entries: tail.entries}))

	// Source cleanup can find a matching attempt and still must leave the full
	// audit receipt intact: the audit method is outside its two-method filter.
	source := uuid.NewString()
	effect := business.AuditDeliveryEffectID(business.JournalStart)
	cleanupCtx := receipts.WithEffect(testCtx, receipts.Effect{
		ID: effect, Tenant: installation, Method: business.SourceOperationMethod, RequestDigest: []byte("source fixture"),
	})
	require.NoError(t, testStore.WithOrgTx(cleanupCtx, installation, func(ctx context.Context) error {
		_, err := testStore.CreateSourceOperationAttempt(ctx, business.SourceOperationAttempt{
			OrgID: installation, EffectID: effect, ActorID: caller.PrincipalID, SourceID: source,
			Operation: "example", DeclarationDigest: "example", RequestDigest: []byte("source fixture"),
		})
		if err != nil {
			return err
		}
		if err := svc.RecordSourceOperationReceipt(ctx, &gen.InvokeSourceOperationResponse{OutputJson: "{}"}); err != nil {
			return err
		}
		removed, err := testStore.PruneSourceOperationReceipts(ctx, installation, source, time.Now().Add(time.Hour))
		require.EqualValues(t, 1, removed, "the expired datasource receipt is removed")
		return err
	}))
	saved, found, err := receiptStore.Lookup(testCtx, installation, effect, business.DeliverAuditEventsMethod)
	require.NoError(t, err)
	require.True(t, found)
	var retained gen.DeliverAuditEventsResponse
	require.NoError(t, proto.Unmarshal(saved.Response, &retained))
	require.True(t, proto.Equal(first, &retained), "cleanup preserves coverage and the committed response")

	// Backdate the delivered records and a later, undelivered record. Running
	// real warehouse retention retires only the four records proven above.
	undelivered := auditJournalEnvelopeOf(t, seedUser(t), orgB)
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return auditJournalTransport(t).Publish(ctx, storetx.Tx(ctx), undelivered)
	}))
	ids = append(ids, later.GetId(), undelivered.GetId())
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := storetx.Tx(ctx).Exec(ctx, `UPDATE domain_events SET created_at = clock_timestamp() - interval '960 hours' WHERE id::text = ANY($1::text[])`, ids)
		return err
	}))
	postgres, err := business.NewService(testStore)
	require.NoError(t, err)
	postgresDeleted, err := postgres.RunRetention(testCtx)
	require.NoError(t, err)
	require.NotContains(t, postgresDeleted, "audit_journal")
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var remaining int
		err := storetx.Tx(ctx).QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE id::text = ANY($1::text[])`, ids).Scan(&remaining)
		require.Equal(t, 5, remaining, "Postgres mode leaves every journal entry in place")
		return err
	}))
	deleted, err := svc.RunRetention(testCtx)
	require.NoError(t, err)
	require.EqualValues(t, 4, deleted["audit_journal"])
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		var remaining []string
		rows, err := storetx.Tx(ctx).Query(ctx, `SELECT id::text FROM domain_events WHERE id::text = ANY($1::text[])`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			remaining = append(remaining, id)
		}
		require.Equal(t, []string{undelivered.GetId()}, remaining)
		return rows.Err()
	}))
}
