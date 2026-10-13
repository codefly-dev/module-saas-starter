//go:build !pure

package infra_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sort"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/infra/storetx"

	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
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
// The replay is the receipt guard's, which this test runs by hand (Serialize,
// Lookup, then the handler under its effect) because the guard admits only methods
// marked by the generated descriptors, and DeliverAuditEvents has none until the
// protos are generated. The marked-method path itself is not exercised here.
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

	// handle runs one delivery as the receipt guard would: serialize on the effect,
	// answer a committed one from its receipt, otherwise run the handler under it.
	handlerRuns := 0
	handle := func(from, to string) (*wrapperspb.Int32Value, error) {
		effectID := business.AuditDeliveryEffectID(from)
		digest := sha256.Sum256([]byte(from + "\n" + to))
		held, err := receiptStore.Serialize(testCtx, installation, effectID, business.DeliverAuditEventsMethod)
		if err != nil {
			return nil, err
		}
		defer held.Release()
		if saved, found, err := held.Lookup(testCtx); err != nil || found {
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(saved.RequestDigest, digest[:]) {
				return nil, receipts.ErrEffectIDReused
			}
			replayed := &wrapperspb.Int32Value{}
			return replayed, proto.Unmarshal(saved.Response, replayed)
		}
		handlerRuns++
		ctx := receipts.WithEffect(testCtx, receipts.Effect{ID: effectID, Tenant: installation, Method: business.DeliverAuditEventsMethod, RequestDigest: digest[:]})
		var out *wrapperspb.Int32Value
		_, err = svc.DeliverAuditEvents(ctx, from, to, func(ctx context.Context, r business.AuditDeliveryResult) error {
			out = wrapperspb.Int32(int32(r.Delivered))
			return svc.RecordSourceOperationReceipt(ctx, out)
		})
		return out, err
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
	require.EqualValues(t, 3, first.Value)
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
	require.EqualValues(t, 2, overlap.Value)
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
	require.EqualValues(t, 1, done.Value)

	// Delivering wrote no audit entry of its own: nothing after the last record
	// this test published, but that record.
	tail := drainJournal(t, svc, caller, position(2), business.AuditRecordedEventType)
	require.Equal(t, []string{later.GetId()}, eventIDs(business.ModuleReadJournalResult{Entries: tail.entries}))
}
