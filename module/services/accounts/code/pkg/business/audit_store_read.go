package business

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The read half of the audit store (ADR 0009). Under AUDIT_SINK=postgres and
// AUDIT_SINK=both the activity list, aggregation, export and readable-source
// queries read audit_events under row-level security, exactly as before. Under
// a swap value they read the store of record instead, which row-level security
// does not reach: every read names its scope explicitly (AuditReadScope), and
// the store refuses one that names none.

// ErrAuditReadUnscoped is the refusal of a read that names no scope.
var ErrAuditReadUnscoped = errors.New("audit read: no scope; a read names its organization, or is explicitly a platform read")

// AuditReadScope is whose events a read may return. It is either one
// organization's events, or — explicitly — the platform's view, which spans
// every organization and the platform's own events (no organization). The zero
// value is no scope, and every store refuses it.
//
// The platform scope is exactly what Postgres serves under WithControlPlane
// today: a read whose query names no organization, which the transport admits
// only for a platform admin.
type AuditReadScope struct {
	orgID    string
	platform bool
}

// OrganizationAuditScope scopes a read to one organization's events.
func OrganizationAuditScope(orgID string) AuditReadScope {
	return AuditReadScope{orgID: orgID}
}

// PlatformAuditScope is the explicit platform read: every organization's events
// and the platform's own. Only the platform-admin path takes it.
func PlatformAuditScope() AuditReadScope {
	return AuditReadScope{platform: true}
}

// OrgID is the organization an organization-scoped read is confined to; empty
// for the platform scope.
func (s AuditReadScope) OrgID() string { return s.orgID }

// Platform reports whether the read is the explicit platform read.
func (s AuditReadScope) Platform() bool { return s.platform }

// Validate refuses the zero scope.
func (s AuditReadScope) Validate() error {
	if s.platform == (s.orgID != "") {
		return ErrAuditReadUnscoped
	}
	return nil
}

// String names the scope for errors and logs.
func (s AuditReadScope) String() string {
	switch {
	case s.platform:
		return "platform"
	case s.orgID != "":
		return "organization " + s.orgID
	default:
		return "unscoped"
	}
}

// auditReadScopeFor is the scope of a Service read of q: its organization, or
// the platform scope when it names none — the read Postgres serves under
// WithControlPlane, which the transport admits only for a platform admin.
func auditReadScopeFor(q AuditQuery) AuditReadScope {
	if q.OrgID == "" {
		return PlatformAuditScope()
	}
	return OrganizationAuditScope(q.OrgID)
}

// AuditEventTypeFacts is what a read needs to know about an event type it does
// not hold itself: the category and namespace its audit_event_types row names.
type AuditEventTypeFacts struct {
	Category  string
	Namespace string
}

// AuditEventTypeIndex is the audit_event_types projection by type name — the
// registry's code-owned and declared types alike. In Postgres the category and
// namespace filters and the category dimension read that table in SQL; a
// store of record outside Postgres takes the same facts from this index, which
// the service reads from the same table.
type AuditEventTypeIndex map[string]AuditEventTypeFacts

// NewAuditEventTypeIndex indexes audit_event_types rows.
func NewAuditEventTypeIndex(rows []AuditEventTypeRow) AuditEventTypeIndex {
	index := make(AuditEventTypeIndex, len(rows))
	for _, row := range rows {
		index[row.Name] = AuditEventTypeFacts{Category: row.Category, Namespace: row.Namespace}
	}
	return index
}

// AuditRead is one read of the store of record: whose events, which ones, and
// the event-type facts the query needs.
type AuditRead struct {
	Scope AuditReadScope
	Query AuditQuery
	// Types is set when the query filters on a category or namespace, or the
	// aggregation groups or counts by category. A store that needs it and finds
	// it nil refuses the read rather than treating every type as unknown.
	Types AuditEventTypeIndex
}

// Validate checks the read's scope and that the query does not reach past it:
// an organization-scoped read's query names that organization, and a platform
// read's query names none.
func (r AuditRead) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if r.Query.OrgID != r.Scope.orgID {
		return fmt.Errorf("audit read: query organization %q is outside the %s scope", r.Query.OrgID, r.Scope)
	}
	if r.Query.CollectionID != "" {
		return errors.New("uncompiled collection filter: reader authorization required")
	}
	return nil
}

// AuditReader is the read half of AuditStore: the activity list, aggregation and
// export. Every read returns each event once by event id, whatever the store
// holds after a redelivery.
type AuditReader interface {
	// ListAuditEvents returns one page, newest first — by occurrence, then by
	// event id, both descending — and the token of the next page, empty after
	// the last. A zero page size is 50, as it is in Postgres.
	ListAuditEvents(ctx context.Context, read AuditRead) ([]AuditEntry, string, error)
	// AggregateAuditEvents groups the matching events per spec, which the caller
	// has validated.
	AggregateAuditEvents(ctx context.Context, read AuditRead, spec AuditAggregationSpec) ([]AuditAggregateBucket, error)
	// ExportAuditEvents returns every matching event, newest first.
	ExportAuditEvents(ctx context.Context, read AuditRead) ([]AuditEntry, error)
}

// AuditSourceSyncEvent is the newest "a sync was requested" event of one
// source: when, and the actor id the event names (empty when it names none).
type AuditSourceSyncEvent struct {
	RequestedAt time.Time
	ActorID     string
}

// AuditSourceSyncReader is the readable-source query of the store of record
// (ADR 0008 keeps a sync's actor in the audit trail alone). Postgres answers it
// in one query joined to principals inside the source-read snapshot
// (Store.LatestSourceSyncRequests); a store outside Postgres returns the actor
// ids, and the service joins their display names from Postgres.
type AuditSourceSyncReader interface {
	// LatestSourceSyncEvents returns, per source that has one, its newest
	// EventDatasourceSourceSynced event in the organization the scope names.
	LatestSourceSyncEvents(ctx context.Context, scope AuditReadScope, sources []string) (map[string]AuditSourceSyncEvent, error)
}

// EncodeAuditPageToken / DecodeAuditPageToken carry the keyset position of the
// activity list: the last returned event's (occurrence, id). The token is
// opaque to callers — base64 of "<RFC3339Nano>|<id>" — and every store reads
// the same token, so a page boundary means the same thing in all of them.
func EncodeAuditPageToken(occurredAt time.Time, id string) string {
	raw := occurredAt.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeAuditPageToken reads a token EncodeAuditPageToken wrote.
func DecodeAuditPageToken(token string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return occurredAt, parts[1], nil
}

// auditReadNeedsTypes reports whether a read of q, aggregated per spec when it
// is not nil, needs the event-type index.
func auditReadNeedsTypes(q AuditQuery, spec *AuditAggregationSpec) bool {
	if q.Category != "" || q.Namespace != "" {
		return true
	}
	if spec == nil {
		return false
	}
	for _, dimension := range spec.GroupBy {
		if dimension == "category" {
			return true
		}
	}
	for _, metric := range spec.Metrics {
		if metric.Op == "count_distinct" && metric.Field == "category" {
			return true
		}
	}
	return false
}

// postgresAuditReader is the Postgres implementation of AuditReader — the reads
// every postgres and both deployment has always made, and the reference the
// other stores are measured against. Each read runs in its own transaction: the
// organization's (row-level security admits its rows) or, for the platform
// scope, the control plane's (which spans tenants). Postgres reads the
// event-type facts itself, in SQL, so it ignores AuditRead.Types.
type postgresAuditReader struct {
	store Store
}

// NewPostgresAuditReader is the Postgres reference reader over store.
func NewPostgresAuditReader(store Store) AuditReader {
	return postgresAuditReader{store: store}
}

func (r postgresAuditReader) within(ctx context.Context, scope AuditReadScope, fn func(context.Context) error) error {
	if scope.Platform() {
		return r.store.WithControlPlane(ctx, fn)
	}
	return r.store.WithOrgTx(ctx, scope.OrgID(), fn)
}

func (r postgresAuditReader) ListAuditEvents(ctx context.Context, read AuditRead) ([]AuditEntry, string, error) {
	if err := read.Validate(); err != nil {
		return nil, "", err
	}
	var entries []AuditEntry
	var nextToken string
	err := r.within(ctx, read.Scope, func(ctx context.Context) error {
		ev, nt, _, err := r.store.QueryAuditLog(ctx, read.Query)
		entries, nextToken = ev, nt
		return err
	})
	return entries, nextToken, err
}

func (r postgresAuditReader) AggregateAuditEvents(ctx context.Context, read AuditRead, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if err := read.Validate(); err != nil {
		return nil, err
	}
	var out []AuditAggregateBucket
	err := r.within(ctx, read.Scope, func(ctx context.Context) error {
		var err error
		out, err = r.store.AggregateAuditLog(ctx, read.Query, spec)
		return err
	})
	return out, err
}

// ExportAuditEvents pages the list 100 events at a time, each page in its own
// transaction, until it runs out.
func (r postgresAuditReader) ExportAuditEvents(ctx context.Context, read AuditRead) ([]AuditEntry, error) {
	var all []AuditEntry
	page := read
	page.Query.PageSize = 100
	page.Query.PageToken = ""
	for {
		entries, nextToken, err := r.ListAuditEvents(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
		if nextToken == "" || len(entries) == 0 {
			return all, nil
		}
		page.Query.PageToken = nextToken
	}
}
