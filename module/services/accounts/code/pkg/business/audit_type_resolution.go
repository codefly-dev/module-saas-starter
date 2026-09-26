package business

import (
	"context"
	"fmt"
)

// One lookup for what the audit registry says about an event type.
//
// The registry has two halves: the code-owned catalog, compiled in, and the
// types registered solutions declared, stored as audit_event_types rows
// (solution_audit_events.go). Every path that needs a type's schema — to stamp
// its version, to check a payload, to label its category, and above all to strip
// its PII before an event leaves the audit store — resolves it here, so a
// declared type is governed exactly like a catalog one and nothing reads only
// half the registry.
//
// A catalog type never costs a read. Only a type with the shape a solution
// could have declared reaches the store, once per resolver.

// DeclaredAuditEventTypeReader reads one solution-declared type, nil when none
// is admitted under that name. The Store satisfies it; it reads on the ambient
// transaction when there is one.
type DeclaredAuditEventTypeReader interface {
	GetDeclaredAuditEventType(ctx context.Context, eventType EventType) (*DeclaredAuditEventType, error)
}

// ResolvedAuditEvent is what the registry says about one event type: its
// governing definition, and whether it is registered at all.
type ResolvedAuditEvent struct {
	Definition AuditEventDefinition
	Registered bool
}

// Category is the type's category, empty for an unregistered type.
func (r ResolvedAuditEvent) Category() string {
	if !r.Registered {
		return ""
	}
	return string(r.Definition.Category)
}

// Redact returns a copy of payload with every field the definition marks PII
// removed. Every path that sends an event outside the audit store uses it, so
// downstream sinks never receive personally identifying fields. An unregistered
// type is redacted whole (fail closed): without a schema no field is known safe.
func (r ResolvedAuditEvent) Redact(payload map[string]any) map[string]any {
	if len(payload) == 0 {
		return payload
	}
	if !r.Registered {
		return map[string]any{}
	}
	piiFields := make(map[string]struct{})
	for _, f := range r.Definition.Fields {
		if f.PII {
			piiFields[f.Name] = struct{}{}
		}
	}
	if len(piiFields) == 0 {
		return payload
	}
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if _, redacted := piiFields[k]; redacted {
			continue
		}
		out[k] = v
	}
	return out
}

// Validate checks a payload against the definition. An unregistered type is
// an error.
func (r ResolvedAuditEvent) Validate(t EventType, payload map[string]any) error {
	if !r.Registered {
		return fmt.Errorf("audit: unregistered event type %q", t)
	}
	return validatePayloadFields(t, r.Definition.Fields, payload)
}

// AuditEventResolver resolves event types against the whole registry. It
// remembers each answer for its own lifetime, so one operation over many events
// reads each declared type once; make one per operation, never per process, so
// a type admitted or extended since is seen by the next one.
type AuditEventResolver struct {
	reader DeclaredAuditEventTypeReader
	memo   map[EventType]ResolvedAuditEvent
}

// NewAuditEventResolver returns a resolver over the catalog and the declared
// types reader serves. A nil reader resolves the catalog alone.
func NewAuditEventResolver(reader DeclaredAuditEventTypeReader) *AuditEventResolver {
	return &AuditEventResolver{reader: reader, memo: map[EventType]ResolvedAuditEvent{}}
}

// Resolve returns what the registry says about t. An error means the declared
// half could not be read, which is not the same as "unregistered": a caller
// must not redact, refuse or label on it.
func (r *AuditEventResolver) Resolve(ctx context.Context, t EventType) (ResolvedAuditEvent, error) {
	if d, ok := LookupAuditEvent(t); ok {
		return ResolvedAuditEvent{Definition: d, Registered: true}, nil
	}
	if resolved, ok := r.memo[t]; ok {
		return resolved, nil
	}
	var resolved ResolvedAuditEvent
	if r.reader != nil && isDeclarableAuditEventType(t) {
		declared, err := r.reader.GetDeclaredAuditEventType(ctx, t)
		if err != nil {
			return ResolvedAuditEvent{}, fmt.Errorf("audit: resolve declared event type %q: %w", t, err)
		}
		if declared != nil {
			resolved = ResolvedAuditEvent{Definition: declared.Definition(), Registered: true}
		}
	}
	r.memo[t] = resolved
	return resolved, nil
}

// AuditEventResolver returns a resolver over this service's store, for one
// operation.
func (s *Service) AuditEventResolver() *AuditEventResolver {
	return NewAuditEventResolver(s.store)
}
