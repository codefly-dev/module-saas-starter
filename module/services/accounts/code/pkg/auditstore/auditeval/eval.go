// Package auditeval evaluates an audit read in the service, over the events a
// store of record returned: the predicates of AuditQuery, the scope, newest-
// first paging, export, and AggregateAuditLog's aggregation — each with the
// semantics the Postgres reads give it, so a store that cannot run them itself
// answers exactly as Postgres does.
//
// A store pushes down what it can (scope, time window, event type) to read
// less; the evaluation here is still the authority on every predicate,
// including the scope, so a store that returned too much still answers
// correctly. Every event is counted once by event id: a store of record may
// hold an event more than once after a redelivery (ADR 0009).
package auditeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"

	"github.com/google/uuid"
)

// Event is one stored event as a store returned it: the envelope, and the
// canonical details when the store had them. A content-class event's details
// are kept for the content window only; past it the event has no details, and
// every payload predicate, dimension and metric reads it as having no payload.
type Event struct {
	// Entry is the envelope. Its Payload is ignored: Details is the payload.
	Entry      business.AuditEntry
	Details    string
	HasDetails bool
	// DetailsSHA256 is the hash the event's details carry wherever they are
	// kept; a store joins details to the event by it as well as by id.
	DetailsSHA256 string

	decoded    map[string]any
	decodeErr  error
	decodedSet bool
}

// payload is the details decoded with json.Number, or nil when there are none.
func (e *Event) payload() (map[string]any, error) {
	if !e.HasDetails {
		return nil, nil
	}
	if !e.decodedSet {
		e.decoded, e.decodeErr = decodeDetails(e.Details)
		e.decodedSet = true
	}
	return e.decoded, e.decodeErr
}

// Result is the entry a read returns for the event: the envelope with its
// payload decoded exactly as the Postgres reads decode a stored payload
// (encoding/json into map[string]any). An event without details has no payload.
func (e *Event) Result() business.AuditEntry {
	entry := e.Entry
	entry.Payload = nil
	if e.HasDetails {
		var payload map[string]any
		if json.Unmarshal([]byte(e.Details), &payload) == nil {
			entry.Payload = payload
		}
	}
	return entry
}

// newer reports whether a sorts before b in the activity list: by occurrence,
// then by event id, both descending.
func newer(a, b business.AuditEntry) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// Matcher decides whether an event is one a read returns.
type Matcher struct {
	scope      business.AuditReadScope
	actorID    string
	eventType  string
	eventTypes map[string]bool
	category   string
	namespace  string
	types      business.AuditEventTypeIndex
	resource   string
	resourceID string
	clientID   string
	payload    any
	from, to   *time.Time
}

// NewMatcher compiles a read. It refuses a read without a scope, a query that
// reaches past its scope, a category or namespace filter without the
// event-type index, and an organization or actor id that is not a uuid —
// which Postgres refuses too, as invalid input for a uuid column.
func NewMatcher(read business.AuditRead) (*Matcher, error) {
	if err := read.Validate(); err != nil {
		return nil, err
	}
	q := read.Query
	m := &Matcher{
		scope:      read.Scope,
		eventType:  q.EventType,
		category:   q.Category,
		namespace:  q.Namespace,
		types:      read.Types,
		resource:   q.Resource,
		resourceID: q.ResourceID,
		clientID:   q.ClientID,
	}
	if org := read.Scope.OrgID(); org != "" {
		if _, err := uuid.Parse(org); err != nil {
			return nil, fmt.Errorf("audit read: organization %q is not a uuid", org)
		}
	}
	if q.ActorID != "" {
		actor, err := uuid.Parse(q.ActorID)
		if err != nil {
			return nil, fmt.Errorf("audit read: actor %q is not a uuid", q.ActorID)
		}
		m.actorID = actor.String()
	}
	if len(q.EventTypes) > 0 {
		m.eventTypes = make(map[string]bool, len(q.EventTypes))
		for _, eventType := range q.EventTypes {
			m.eventTypes[eventType] = true
		}
	}
	if (q.Category != "" || q.Namespace != "") && read.Types == nil {
		return nil, errors.New("audit read: a category or namespace filter needs the event-type index")
	}
	if len(q.PayloadContains) > 0 {
		payload, err := normalizeJSON(q.PayloadContains)
		if err != nil {
			// A filter that cannot be bound is never "no filter": dropping it would
			// widen the result past what the caller was cleared to read.
			return nil, fmt.Errorf("audit: cannot bind payload filter: %w", err)
		}
		m.payload = payload
	}
	m.from = MicrosecondBound(q.From)
	m.to = MicrosecondBound(q.To)
	return m, nil
}

// MicrosecondBound is a query's time bound at the precision Postgres compares
// it at: a bound parameter is sent in whole microseconds, truncated.
func MicrosecondBound(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	bound := t.Truncate(time.Microsecond)
	return &bound
}

// Scope is the read's scope.
func (m *Matcher) Scope() business.AuditReadScope { return m.scope }

// ActorID is the actor filter as stored: a canonical lowercase uuid, or "".
func (m *Matcher) ActorID() string { return m.actorID }

// From and To are the read's inclusive time bounds, at microsecond precision.
func (m *Matcher) From() *time.Time { return m.from }

// To is the read's inclusive upper bound, or nil.
func (m *Matcher) To() *time.Time { return m.to }

// EventTypes is the set of event types the read can match, and whether the
// read restricts the type at all — the scalar filter, the set filter and the
// category and namespace filters intersected. A store pushes it down.
func (m *Matcher) EventTypes() ([]string, bool) {
	var candidates map[string]bool
	restrict := func(allowed func(string) bool, from map[string]bool) {
		next := map[string]bool{}
		for name := range from {
			if allowed(name) {
				next[name] = true
			}
		}
		candidates = next
	}
	switch {
	case m.eventType != "":
		candidates = map[string]bool{m.eventType: true}
	case m.eventTypes != nil:
		candidates = map[string]bool{}
		for name := range m.eventTypes {
			candidates[name] = true
		}
	case m.category != "" || m.namespace != "":
		candidates = map[string]bool{}
		for name := range m.types {
			candidates[name] = true
		}
	default:
		return nil, false
	}
	if m.eventTypes != nil {
		restrict(func(name string) bool { return m.eventTypes[name] }, candidates)
	}
	if m.category != "" {
		restrict(func(name string) bool { facts, ok := m.types[name]; return ok && facts.Category == m.category }, candidates)
	}
	if m.namespace != "" {
		restrict(func(name string) bool { facts, ok := m.types[name]; return ok && facts.Namespace == m.namespace }, candidates)
	}
	out := make([]string, 0, len(candidates))
	for name := range candidates {
		out = append(out, name)
	}
	sortStrings(out)
	return out, true
}

// NeedsPayload reports whether the read filters on the payload, so a store
// must join every candidate's details before Match can decide.
func (m *Matcher) NeedsPayload() bool { return m.payload != nil }

// PayloadFilter is the payload filter as Match evaluates it — the query's
// PayloadContains read back the way Postgres binds it as jsonb, numbers as
// json.Number — or nil when the read has none. A store that pushes part of it
// down reads it here, so what it pushes is exactly what Match decides.
func (m *Matcher) PayloadFilter() any { return m.payload }

// InScope reports whether an event belongs to the read's scope: its
// organization's, or for the platform scope any event at all.
func (m *Matcher) InScope(entry business.AuditEntry) bool {
	return m.scope.Platform() || entry.OrgID == m.scope.OrgID()
}

// Match reports whether an event is in the read's scope and matches every
// predicate of its query.
func (m *Matcher) Match(event *Event) (bool, error) {
	entry := event.Entry
	if !m.InScope(entry) {
		return false, nil
	}
	if m.actorID != "" && entry.ActorID != m.actorID {
		return false, nil
	}
	eventType := string(entry.EventType)
	if m.eventType != "" && eventType != m.eventType {
		return false, nil
	}
	if m.eventTypes != nil && !m.eventTypes[eventType] {
		return false, nil
	}
	if m.category != "" || m.namespace != "" {
		facts, ok := m.types[eventType]
		if !ok || (m.category != "" && facts.Category != m.category) || (m.namespace != "" && facts.Namespace != m.namespace) {
			return false, nil
		}
	}
	if m.resource != "" && entry.Resource != m.resource {
		return false, nil
	}
	if m.resourceID != "" && entry.ResourceID != m.resourceID {
		return false, nil
	}
	if m.clientID != "" && entry.ClientID != m.clientID {
		return false, nil
	}
	if m.from != nil && entry.CreatedAt.Before(*m.from) {
		return false, nil
	}
	if m.to != nil && entry.CreatedAt.After(*m.to) {
		return false, nil
	}
	if m.payload != nil {
		payload, err := event.payload()
		if err != nil {
			return false, fmt.Errorf("audit read: details of event %s: %w", entry.ID, err)
		}
		if payload == nil || !jsonbContains(payload, m.payload) {
			return false, nil
		}
	}
	return true, nil
}

// Dedupe remembers which event ids a read has already counted.
type Dedupe struct {
	uuids map[uuid.UUID]struct{}
	other map[string]struct{}
}

// NewDedupe is an empty set.
func NewDedupe() *Dedupe {
	return &Dedupe{uuids: map[uuid.UUID]struct{}{}, other: map[string]struct{}{}}
}

// First reports whether id is seen for the first time, and remembers it.
func (d *Dedupe) First(id string) bool {
	if parsed, err := uuid.Parse(id); err == nil && parsed.String() == id {
		if _, seen := d.uuids[parsed]; seen {
			return false
		}
		d.uuids[parsed] = struct{}{}
		return true
	}
	if _, seen := d.other[id]; seen {
		return false
	}
	d.other[id] = struct{}{}
	return true
}
