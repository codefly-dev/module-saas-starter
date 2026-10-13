package business

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/events"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AuditRecordedEventType is the journal type of the warehouse backend: one
// event per audit entry, carrying the whole entry, published in the caller's
// transaction beside the mutation it records. It is code-owned and `internal`:
// it is read only by the platform's own reader (ReadJournal, for a cross-tenant
// module principal) and by DeliverAuditEvent, never fanned out to a subscriber,
// never relayed to a webhook, and never subscribable (the platform namespace is
// not subscribable by a module, and no pattern may match an internal type).
//
// It is not an audit event type: it has no audit_event_types row, and
// publishing it writes no audit entry. The audit entry's own webhook event, for
// an organization-scoped type declared external, is published separately and is
// unchanged.
const AuditRecordedEventType = "saas.audit.recorded.v1"

// maxAuditJournalData is the largest `data` a domain event carries (EVENTS.md:
// 960 KiB, below the 1 MiB job_messages.payload cap). domain_events.data has no
// CHECK of its own, so the producer enforces it.
const maxAuditJournalData = 960 * 1024

// ErrAuditEntryTooLarge is the refusal of an entry whose journal record would
// exceed the data limit. The journal is the audit entry's only copy until a
// delivery appends it, so the entry is not truncated and not dropped: the emit
// fails, and the caller's transaction with it, exactly as a failed append does.
var ErrAuditEntryTooLarge = errors.New("audit: the entry exceeds the journal JSON or encoded page limit")

// auditJournalNamespace derives the journal event id from the audit entry id.
// The two cannot be one id: an organization-scoped entry of an external type is
// also published as a webhook event under the entry's own id (the id an endpoint
// deduplicates on), and an event id is unique in the journal.
var auditJournalNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("urn:saas:"+AuditRecordedEventType))

// AuditJournalEventID is the id of the journal event that carries the audit
// entry id. It is deterministic, so a retried publish of one entry is one event.
func AuditJournalEventID(entryID string) string {
	return uuid.NewSHA1(auditJournalNamespace, []byte(entryID)).String()
}

// auditJournalRecord is the `data` of the journal event: the whole entry, in the
// JSON the delivery reads back. Its field names are the contract between the
// publisher and DeliverAuditEvent.
type auditJournalRecord struct {
	ID             string         `json:"id"`
	EventType      string         `json:"event_type"`
	SchemaVersion  int            `json:"schema_version"`
	ActorID        string         `json:"actor_id,omitempty"`
	ActorType      string         `json:"actor_type"`
	Resource       string         `json:"resource"`
	ResourceID     string         `json:"resource_id,omitempty"`
	OrganizationID string         `json:"organization_id,omitempty"`
	Payload        map[string]any `json:"payload"`
	IPAddress      string         `json:"ip_address,omitempty"`
	ImpersonatedBy string         `json:"impersonated_by,omitempty"`
	IsImpersonated bool           `json:"is_impersonated"`
	ClientID       string         `json:"client_id,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

// NewAuditJournalEnvelope is the journal event of one audit entry. The entry
// must already carry its id, its occurrence and its schema version, which the
// emitter backfills before it gets here.
//
// There is no partition key. A partition is a promise of FIFO within it, bought
// with an advisory lock held until the producer's transaction commits, and
// nothing reads the journal in partition order. The tenant is the entry's
// organization, empty for an entry with no organization.
//
// The envelope's actor is the entry's actor id: for a person it is their user id,
// which is also their principal id. publish_domain_event reads it to admit an
// entry with no organization from a user-scoped transaction (a caller running as
// app_tenant with only app.current_user_id set): such an event is admitted only
// when its actor is that user, the rule audit_events_tenant applies to the same
// entry's row (migration 37).
func NewAuditJournalEnvelope(entry AuditEntry) (*events.EventEnvelope, error) {
	if entry.ID == "" {
		return nil, errors.New("audit: a journal event needs the entry id")
	}
	version := entry.SchemaVersion
	if version == 0 {
		version = 1 // what audit_events records for an entry with no registered version
	}
	payload := entry.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	data, err := json.Marshal(auditJournalRecord{
		ID: entry.ID, EventType: string(entry.EventType), SchemaVersion: version,
		ActorID: entry.ActorID, ActorType: entry.ActorType,
		Resource: entry.Resource, ResourceID: entry.ResourceID, OrganizationID: entry.OrgID,
		Payload: payload, IPAddress: entry.IPAddress,
		ImpersonatedBy: entry.ImpersonatedBy, IsImpersonated: entry.IsImpersonated,
		ClientID: entry.ClientID, OccurredAt: entry.CreatedAt.UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("audit: journal record of %s: %w", entry.ID, err)
	}
	if len(data) > maxAuditJournalData {
		return nil, fmt.Errorf("%w: entry %s is %d bytes", ErrAuditEntryTooLarge, entry.ID, len(data))
	}
	// JSON arrays of small numbers grow substantially as protobuf Struct values.
	// Refuse an entry before commit if ReadJournal could never return it alone.
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("audit: journal object: %w", err)
	}
	projected, err := structpb.NewStruct(object)
	if err != nil {
		return nil, fmt.Errorf("audit: journal object: %w", err)
	}
	if size := proto.Size(projected); size > ModuleJournalMaxWireBytes-1024 {
		return nil, fmt.Errorf("%w: entry %s requires %d protobuf bytes", ErrAuditEntryTooLarge, entry.ID, size)
	}
	return &events.EventEnvelope{
		Id:              AuditJournalEventID(entry.ID),
		Type:            AuditRecordedEventType,
		Source:          domainEventSource,
		Subject:         entry.ID,
		Specversion:     "1.0",
		Datacontenttype: "application/json",
		Time:            timestamppb.New(entry.CreatedAt.UTC()),
		Data:            data,
		TenantId:        entry.OrgID,
		// The same value audit_events.actor_id records for the entry.
		ActorPrincipalId: entry.ActorID,
		SchemaVersion:    1,
	}, nil
}

// AuditEntryFromJournal reads the entry back out of a journal event's `data`.
// Numbers keep the text they were written with (json.Number), so an amount in a
// payload reaches the storage exactly as it was recorded.
func AuditEntryFromJournal(data []byte) (AuditEntry, error) {
	var record auditJournalRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return AuditEntry{}, fmt.Errorf("audit: journal record: %w", err)
	}
	if record.ID == "" || record.EventType == "" {
		return AuditEntry{}, errors.New("audit: journal record names no entry")
	}
	return AuditEntry{
		ID: record.ID, EventType: EventType(record.EventType), SchemaVersion: record.SchemaVersion,
		ActorID: record.ActorID, ActorType: record.ActorType,
		Resource: record.Resource, ResourceID: record.ResourceID, OrgID: record.OrganizationID,
		Payload: record.Payload, IPAddress: record.IPAddress,
		ImpersonatedBy: record.ImpersonatedBy, IsImpersonated: record.IsImpersonated,
		ClientID: record.ClientID, CreatedAt: record.OccurredAt,
	}, nil
}
