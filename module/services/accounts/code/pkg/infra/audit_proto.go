package infra

import (
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// AuditEntryToProto converts a business AuditEntry to the proto AuditEvent.
// Payloads are surfaced in full here: the query/UI path is an authenticated
// admin read of the caller's own tenant, not an export, so PII redaction (which
// applies to export/webhook sinks) is deliberately not performed.
//
// resolved is what the registry says about the entry's type
// (business.AuditEventResolver), so a solution-declared type is labelled with
// its category like a catalog one.
func AuditEntryToProto(e business.AuditEntry, resolved business.ResolvedAuditEvent) *gen.AuditEvent {
	event := &gen.AuditEvent{
		Id:            e.ID,
		ActorId:       e.ActorID,
		ActorType:     e.ActorType,
		EventType:     string(e.EventType),
		SchemaVersion: int32(e.SchemaVersion),
		Resource:      e.Resource,
		ResourceId:    e.ResourceID,
		OrgId:         e.OrgID,
		IpAddress:     e.IPAddress,
		CreatedAt:     timestamppb.New(e.CreatedAt),
		ClientId:      e.ClientID,
	}
	event.Category = resolved.Category()
	if len(e.Payload) > 0 {
		if s, err := structpb.NewStruct(e.Payload); err == nil {
			event.Payload = s
		}
	}
	return event
}
