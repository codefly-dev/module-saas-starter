package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Under a swap value (ADR 0009) the emitter's record is a queue row, written
// exactly where the audit_events row was — and nothing else about an emit
// changes. These hold the emitter to that on both of its write paths and on
// the record-only path the writers outside the Service use.

// recordStore splits what the emitter writes by destination.
type recordStore struct {
	Store
	inserted     []AuditEntry
	enqueued     []AuditEntry
	reservations map[string]bool
	controlPlane int
}

func (s *recordStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *recordStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	s.controlPlane++
	return fn(ctx)
}

func (s *recordStore) GetDeclaredAuditEventType(context.Context, EventType) (*DeclaredAuditEventType, error) {
	return nil, nil
}

func (s *recordStore) InsertAuditEvent(_ context.Context, entry AuditEntry) error {
	s.inserted = append(s.inserted, entry)
	return nil
}

func (s *recordStore) EnqueueAuditEvent(_ context.Context, entry AuditEntry) error {
	s.enqueued = append(s.enqueued, entry)
	return nil
}

func (s *recordStore) ReserveAuditIdempotency(_ context.Context, orgID, eventType, key string) (bool, error) {
	if s.reservations == nil {
		s.reservations = map[string]bool{}
	}
	id := orgID + "/" + eventType + "/" + key
	if s.reservations[id] {
		return false, nil
	}
	s.reservations[id] = true
	return true, nil
}

func newRecordEmitter(t *testing.T, store *recordStore, opts ...DurableAuditEmitterOption) (*DurableAuditEmitter, *recordingTransport) {
	t.Helper()
	transport := &recordingTransport{}
	emitter, err := NewDurableAuditEmitter(store, silentProducer{}, append(opts, WithDomainEventTransport(transport))...)
	require.NoError(t, err)
	return emitter, transport
}

// saas.session.revoked is published externally, so a webhook-visible emit is
// observable on the transport.
func externallyPublishedEntry() AuditEntry {
	return AuditEntry{
		OrgID:     teeOrgID,
		ActorID:   NewIDString(),
		ActorType: ActorTypeUser,
		EventType: EventSessionRevoked,
		Resource:  "session",
	}
}

func TestQueuedRecordsEmitTxWritesTheQueueBesideTheDomainEvent(t *testing.T) {
	store := &recordStore{}
	emitter, transport := newRecordEmitter(t, store, WithQueuedRecords())

	require.NoError(t, emitter.EmitTx(context.Background(), externallyPublishedEntry()))

	require.Empty(t, store.inserted, "audit_events receives nothing under a swap value")
	require.Len(t, store.enqueued, 1)
	queued := store.enqueued[0]
	require.NotEmpty(t, queued.ID, "the emitter assigns the id before the record is written")
	require.False(t, queued.CreatedAt.IsZero())
	require.Equal(t, 1, queued.SchemaVersion, "the registered version is stamped as it is on an audit_events row")
	require.Len(t, transport.published, 1, "webhook delivery still fans out from the same transaction")
	require.Equal(t, queued.ID, transport.published[0].GetId(), "the domain event keeps the record's id")
}

func TestDefaultRecordsEmitTxWritesAuditEvents(t *testing.T) {
	store := &recordStore{}
	emitter, transport := newRecordEmitter(t, store)

	require.NoError(t, emitter.EmitTx(context.Background(), externallyPublishedEntry()))

	require.Len(t, store.inserted, 1)
	require.Empty(t, store.enqueued, "nothing writes the queue under postgres or both")
	require.Len(t, transport.published, 1)
}

func TestQueuedRecordsPlatformEventsGoThroughTheQueue(t *testing.T) {
	store := &recordStore{}
	emitter, transport := newRecordEmitter(t, store, WithQueuedRecords())

	entry := externallyPublishedEntry()
	entry.OrgID = ""
	emitter.Emit(context.Background(), entry)

	require.Equal(t, 1, store.controlPlane, "a platform event is written on the control plane")
	require.Len(t, store.enqueued, 1, "a platform event reaches the queue, not just organization events")
	require.Empty(t, store.inserted)
	require.Empty(t, transport.published, "a platform event has no tenant to publish to")
}

func TestQueuedRecordsDuplicateEmitWritesNothing(t *testing.T) {
	store := &recordStore{}
	emitter, transport := newRecordEmitter(t, store, WithQueuedRecords())

	entry := externallyPublishedEntry()
	entry.IdempotencyKey = "op-1"
	require.NoError(t, emitter.EmitTx(context.Background(), entry))
	require.NoError(t, emitter.EmitTx(context.Background(), entry))

	require.Len(t, store.enqueued, 1, "the idempotency reservation stays in Postgres beside the queue row")
	require.Len(t, transport.published, 1)
}

func TestRecordTxWritesOnlyTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   []DurableAuditEmitterOption
		queued bool
	}{
		{name: "postgres"},
		{name: "swap", opts: []DurableAuditEmitterOption{WithQueuedRecords()}, queued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordStore{}
			emitter, transport := newRecordEmitter(t, store, tc.opts...)

			require.NoError(t, emitter.RecordTx(context.Background(), externallyPublishedEntry()))

			if tc.queued {
				require.Len(t, store.enqueued, 1)
				require.Empty(t, store.inserted)
			} else {
				require.Len(t, store.inserted, 1)
				require.Empty(t, store.enqueued)
			}
			require.Empty(t, transport.published, "RecordTx publishes no domain event, as the raw-SQL writers it replaces never did")
		})
	}
}

func TestQueuedRecordsAndTheTeeAreExclusive(t *testing.T) {
	_, err := NewDurableAuditEmitter(&recordStore{}, silentProducer{}, WithQueuedRecords(), WithExternalTee())
	require.ErrorContains(t, err, "exclusive")
}
