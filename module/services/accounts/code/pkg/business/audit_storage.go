package business

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuditStorage is where audit entries are kept and read back. It is the one
// interface the audit write path, the activity list and the aggregation go
// through, so the store of record can be chosen once at startup (AuditService)
// instead of being wired into every caller.
//
// Two implementations are meant to exist, and they differ in one way that the
// interface makes explicit rather than hides:
//
//   - PostgresAuditStorage keeps entries in audit_events. Append joins the
//     caller's transaction, so an entry commits or rolls back with the mutation
//     it records.
//   - A warehouse implementation keeps entries outside Postgres. Append cannot
//     join the caller's transaction, so AuditService never calls it from one:
//     the entry is published to the journal (domain_events) on the caller's
//     transaction, and a later task appends it (DeliverAuditEvent).
//
// The registry of event types and the partition methods stay on Store: they are
// about Postgres tables, not about where entries are kept.
//
// Contract, held for every implementation by auditstoragetest.Run:
//
//   - Append stores one entry. An implementation may be at-least-once: an entry
//     appended twice may be held twice, and every read returns each entry once,
//     by entry id.
//   - List returns one page, newest first by occurrence and then by entry id,
//     both descending, and the token of the next page (empty after the last).
//     A zero page size is 50.
//   - Aggregate groups the matching entries per spec, which the caller has
//     validated.
//   - A read names its scope (AuditRead.Scope). An implementation refuses a read
//     with no scope (ErrAuditReadUnscoped), and one whose query reaches past it,
//     instead of treating a missing scope as "everything".
type AuditStorage interface {
	Append(ctx context.Context, entry AuditEntry) error
	List(ctx context.Context, read AuditRead) ([]AuditEntry, string, error)
	Aggregate(ctx context.Context, read AuditRead, spec AuditAggregationSpec) ([]AuditAggregateBucket, error)
}

// ErrAuditReadUnscoped is the refusal of a read that names no scope.
var ErrAuditReadUnscoped = errors.New("audit read: no scope; a read names its organization, or is explicitly a platform read")

// AuditReadScope is whose entries a read may return. It is either one
// organization's entries, or — explicitly — the platform's view, which spans
// every organization and the platform's own entries (no organization). The zero
// value is no scope, and every storage refuses it.
//
// The platform scope is exactly what Postgres serves under WithControlPlane
// today: a read whose query names no organization, which the transport admits
// only for a platform admin.
type AuditReadScope struct {
	orgID    string
	platform bool
}

// OrganizationAuditScope scopes a read to one organization's entries.
func OrganizationAuditScope(orgID string) AuditReadScope {
	return AuditReadScope{orgID: orgID}
}

// PlatformAuditScope is the explicit platform read: every organization's entries
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

// auditReadScopeFor is the scope of a read of q: its organization, or the
// platform scope when it names none.
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
// storage outside Postgres takes the same facts from this index.
type AuditEventTypeIndex map[string]AuditEventTypeFacts

// NewAuditEventTypeIndex indexes audit_event_types rows.
func NewAuditEventTypeIndex(rows []AuditEventTypeRow) AuditEventTypeIndex {
	index := make(AuditEventTypeIndex, len(rows))
	for _, row := range rows {
		index[row.Name] = AuditEventTypeFacts{Category: row.Category, Namespace: row.Namespace}
	}
	return index
}

// AuditRead is one read of the storage: whose entries, which ones, and the
// event-type facts the query needs.
type AuditRead struct {
	Scope AuditReadScope
	Query AuditQuery
	// Types is set when the query filters on a category or namespace, or the
	// aggregation groups or counts by category, and the storage is not
	// Postgres. A storage that needs it and finds it nil refuses the read
	// rather than treating every type as unknown.
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

// EncodeAuditPageToken / DecodeAuditPageToken carry the keyset position of the
// activity list: the last returned entry's (occurrence, id). The token is opaque
// to callers — base64 of "<RFC3339Nano>|<id>" — and every storage reads the
// same token, so a page boundary means the same thing in all of them.
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

// PostgresAuditStorage is the AuditStorage over audit_events — what every
// deployment has always used. Append writes on the transaction already on ctx;
// each read runs in its own transaction, the organization's (row-level security
// admits its rows) or, for the platform scope, the control plane's (which spans
// tenants). Postgres reads the event-type facts itself, in SQL, so it ignores
// AuditRead.Types.
type PostgresAuditStorage struct {
	store Store
}

// NewPostgresAuditStorage is the Postgres AuditStorage over store.
func NewPostgresAuditStorage(store Store) (*PostgresAuditStorage, error) {
	if store == nil {
		return nil, errors.New("audit: store is required")
	}
	return &PostgresAuditStorage{store: store}, nil
}

var _ AuditStorage = (*PostgresAuditStorage)(nil)

// within runs fn in the transaction the scope names.
func (p *PostgresAuditStorage) within(ctx context.Context, scope AuditReadScope, fn func(context.Context) error) error {
	return withinAuditScope(ctx, p.store, scope, fn)
}

// withinAuditScope runs fn in the Postgres transaction a read's scope names: the
// organization's, which row-level security confines to its rows, or the control
// plane's for the platform scope, which spans tenants. It is the one place a
// read opens its transaction.
func withinAuditScope(ctx context.Context, store Store, scope AuditReadScope, fn func(context.Context) error) error {
	if scope.Platform() {
		return store.WithControlPlane(ctx, fn)
	}
	return store.WithOrgTx(ctx, scope.OrgID(), fn)
}

// Append inserts the entry into audit_events on the caller's ambient
// transaction. It MUST run inside an active transaction: the entry then commits
// or rolls back with the mutation it records.
func (p *PostgresAuditStorage) Append(ctx context.Context, entry AuditEntry) error {
	return p.store.InsertAuditEvent(ctx, entry)
}

// List reads one page of the activity list.
func (p *PostgresAuditStorage) List(ctx context.Context, read AuditRead) ([]AuditEntry, string, error) {
	if err := read.Validate(); err != nil {
		return nil, "", err
	}
	var entries []AuditEntry
	var nextToken string
	err := p.within(ctx, read.Scope, func(ctx context.Context) error {
		var err error
		entries, nextToken, _, err = p.store.QueryAuditLog(ctx, read.Query)
		return err
	})
	return entries, nextToken, err
}

// Aggregate groups the matching entries per spec.
func (p *PostgresAuditStorage) Aggregate(ctx context.Context, read AuditRead, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if err := read.Validate(); err != nil {
		return nil, err
	}
	var out []AuditAggregateBucket
	err := p.within(ctx, read.Scope, func(ctx context.Context) error {
		var err error
		out, err = p.AggregateOnTx(ctx, read, spec)
		return err
	})
	return out, err
}

// AggregateOnTx is Aggregate on the transaction already on ctx, for a caller
// that has to check something and read in one snapshot (the resource-grant gate
// of AggregateAuditLogForReader). The caller owns the scope: ctx's transaction
// is the organization's, or the control plane's.
func (p *PostgresAuditStorage) AggregateOnTx(ctx context.Context, read AuditRead, spec AuditAggregationSpec) ([]AuditAggregateBucket, error) {
	if err := read.Validate(); err != nil {
		return nil, err
	}
	return p.store.AggregateAuditLog(ctx, read.Query, spec)
}
