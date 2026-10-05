package clickhousestore

import (
	"strconv"
	"strings"
	"time"

	"accounts/pkg/auditstore/auditeval"
	"accounts/pkg/business"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// The statements the read half builds. Every value a read carries — the
// deployment, the organization, ids, event types, payload keys and values,
// time bounds, limits — is a server-side query parameter ({name:Type}), sent
// beside the statement and parsed by the server as a value of that type, so no
// value is ever part of the SQL text. Only the statement's shape varies with
// the read: which predicates, dimensions and metrics it has.

// sqlParams collects one statement's query parameters.
type sqlParams struct {
	args []any
}

// paramEscaper escapes a String parameter for the server, which reads a
// parameter value in its escaped text format: a backslash, tab, newline,
// carriage return or NUL in the value would otherwise be read as an escape
// sequence or the end of the value. clickhouse-go sends a top-level String
// parameter as given, so the store escapes it; it quotes and escapes the
// elements of an Array(String) parameter itself.
var paramEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)

func (p *sqlParams) name() string {
	return "p" + strconv.Itoa(len(p.args))
}

// text binds a String.
func (p *sqlParams) text(value string) string {
	name := p.name()
	p.args = append(p.args, clickhouse.Named(name, paramEscaper.Replace(value)))
	return "{" + name + ":String}"
}

// texts binds an Array(String).
func (p *sqlParams) texts(values []string) string {
	name := p.name()
	if values == nil {
		values = []string{}
	}
	p.args = append(p.args, clickhouse.Named(name, values))
	return "{" + name + ":Array(String)}"
}

// The range of the DateTime64(6, 'UTC') columns: the earliest and latest
// instant ClickHouse can store. A time bound outside it has no value of the
// column's type — the driver refuses the Go zero time (the year 1, which a
// zero protobuf timestamp becomes) and the server has none before 1900 — yet
// to Postgres it is a bound like any other.
var (
	columnMin = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	columnMax = time.Date(2299, 12, 31, 23, 59, 59, 999999000, time.UTC)
)

// clampToColumn is at, or the edge of the column's range it lies beyond.
// Every stored value lies inside the range, so a comparison against the edge
// answers as one against the bound itself did: a bound below the range admits
// every row from below, and one above it admits every row from above. The
// service evaluates the original bound again on every row it reads.
func clampToColumn(at time.Time) time.Time {
	switch {
	case at.Before(columnMin):
		return columnMin
	case at.After(columnMax):
		return columnMax
	default:
		return at
	}
}

// instant binds a time at the microsecond precision occurred_at keeps; a finer
// part is truncated, as Postgres truncates a bound parameter. A time outside
// the column's range is bound as the edge it clamps to (clampToColumn).
func (p *sqlParams) instant(at time.Time) string {
	at = clampToColumn(at)
	name := p.name()
	p.args = append(p.args, clickhouse.DateNamed(name, at, clickhouse.MicroSeconds))
	return "{" + name + ":DateTime64(6, 'UTC')}"
}

// count binds a non-negative integer.
func (p *sqlParams) count(n int) string {
	name := p.name()
	p.args = append(p.args, clickhouse.Named(name, strconv.Itoa(n)))
	return "{" + name + ":UInt64}"
}

// where is a conjunction under construction.
type where []string

func (w *where) and(condition string) { *w = append(*w, condition) }

func (w where) String() string {
	if len(w) == 0 {
		return "1"
	}
	return strings.Join(w, " AND ")
}

// scope confines a statement over either table to this deployment always, and
// to the read's organization unless the read is the explicit platform read.
func (s *Store) scope(p *sqlParams, scope business.AuditReadScope) where {
	w := where{"deployment_id = " + p.text(s.deploymentID)}
	if !scope.Platform() {
		w.and("org_id = " + p.text(scope.OrgID()))
	}
	return w
}

// window adds the read's time bounds, both inclusive.
func (w *where) window(p *sqlParams, from, to *time.Time) {
	if from != nil {
		w.and("occurred_at >= " + p.instant(*from))
	}
	if to != nil {
		w.and("occurred_at <= " + p.instant(*to))
	}
}

// eventTypes adds the event types the read can match. It reports false when
// the read can match none, so the statement need not run.
func (w *where) eventTypes(p *sqlParams, m *auditeval.Matcher) bool {
	types, restricted := m.EventTypes()
	switch {
	case !restricted:
	case len(types) == 0:
		return false
	case len(types) == 1:
		w.and("event_type = " + p.text(types[0]))
	default:
		w.and("has(" + p.texts(types) + ", event_type)")
	}
	return true
}

// plan is what a read pushes into SQL.
type plan struct {
	m *auditeval.Matcher
	q business.AuditQuery
	// payload is the payload filter's SQL form over a details column, when
	// every pair of it has one (pushablePayload); nil otherwise.
	payload map[string]any
	// none is set when the read can match no event type at all.
	none bool
}

func newPlan(m *auditeval.Matcher, q business.AuditQuery) plan {
	pl := plan{m: m, q: q}
	if filter, ok := m.PayloadFilter().(map[string]any); ok && pushablePayload(filter) {
		pl.payload = filter
	}
	if types, restricted := m.EventTypes(); restricted && len(types) == 0 {
		pl.none = true
	}
	return pl
}

// pushablePayload reports whether a payload filter has an exact SQL form:
// every key non-empty and every value a string, a boolean or null at the top
// level. jsonb containment of such a filter is "the payload has each key with
// exactly that value", which ClickHouse's JSON functions decide the same way.
// A number (jsonb compares numerically, at any precision) or a nested object or
// array (containment recurses) is evaluated in the service instead.
func pushablePayload(filter map[string]any) bool {
	for key, value := range filter {
		if key == "" {
			return false
		}
		switch value.(type) {
		case string, bool, nil:
		default:
			return false
		}
	}
	return len(filter) > 0
}

// unparsed is true for details ClickHouse cannot parse as JSON — a document
// valid to Postgres that the server's parser refuses, such as an integer
// beyond 64 bits. Every JSON function reads such a document as empty, so a
// statement that pushes payload predicates down treats it as unknown: a
// candidate the service evaluates, never a silent miss.
func unparsed(column string) string {
	return "(" + column + " != '' AND NOT isValidJSON(" + column + "))"
}

// payloadMatches is the pushed payload filter over a details column: true when
// the details hold every pair exactly.
func payloadMatches(p *sqlParams, column string, filter map[string]any) string {
	keys := make([]string, 0, len(filter))
	for key := range filter {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var terms []string
	for _, key := range keys {
		k := p.text(key)
		switch value := filter[key].(type) {
		case string:
			terms = append(terms, "(JSONType("+column+", "+k+") = 'String' AND JSONExtractString("+column+", "+k+") = "+p.text(value)+")")
		case bool:
			want := "0"
			if value {
				want = "1"
			}
			terms = append(terms, "(JSONType("+column+", "+k+") = 'Bool' AND JSONExtractBool("+column+", "+k+") = "+want+")")
		case nil:
			terms = append(terms, "(JSONType("+column+", "+k+") = 'Null' AND JSONHas("+column+", "+k+"))")
		}
	}
	return "(" + strings.Join(terms, " AND ") + ")"
}

// payloadCandidates is a details column that matches the pushed filter, or
// that ClickHouse cannot parse and so must be decided by the service.
func payloadCandidates(p *sqlParams, column string, filter map[string]any) string {
	return "(" + unparsed(column) + " OR " + payloadMatches(p, column, filter) + ")"
}

// eventsWhere is the condition on the events table for a read: scope, window,
// event types, the envelope filters and — when pushed — the payload filter:
// a security-class event's own details, or a content-class event whose details
// row (by id and hash) satisfies it. ok is false when the read can match
// nothing.
func (s *Store) eventsWhere(p *sqlParams, pl plan) (where, bool) {
	if pl.none {
		return nil, false
	}
	w := s.scope(p, pl.m.Scope())
	w.window(p, pl.m.From(), pl.m.To())
	w.eventTypes(p, pl.m)
	if actor := pl.m.ActorID(); actor != "" {
		w.and("actor_id = " + p.text(actor))
	}
	if pl.q.Resource != "" {
		w.and("resource = " + p.text(pl.q.Resource))
	}
	if pl.q.ResourceID != "" {
		w.and("resource_id = " + p.text(pl.q.ResourceID))
	}
	if pl.q.ClientID != "" {
		w.and("client_id = " + p.text(pl.q.ClientID))
	}
	if pl.payload != nil {
		details := s.detailsWhere(p, pl)
		details.and(payloadCandidates(p, "details", pl.payload))
		w.and("((retention_class = " + p.text(string(business.RetentionSecurity)) + " AND " + payloadCandidates(p, "details", pl.payload) + ")" +
			" OR (retention_class != " + p.text(string(business.RetentionSecurity)) + " AND (event_id, details_sha256) IN (" +
			"SELECT event_id, details_sha256 FROM " + DetailsTable + " WHERE " + details.String() + ")))")
	}
	return w, true
}

// detailsWhere is the condition on the details table that bounds the details
// a read can join: scope, window and event types.
func (s *Store) detailsWhere(p *sqlParams, pl plan) where {
	w := s.scope(p, pl.m.Scope())
	w.window(p, pl.m.From(), pl.m.To())
	w.eventTypes(p, pl.m)
	return w
}

// eventColumns lists the events-table columns every row-returning read
// selects, in the order scanStored reads them.
const eventColumns = "deployment_id, event_id, org_id, actor_id, actor_type, event_type, schema_version, resource, resource_id, " +
	"occurred_at, ip_address, impersonated_by, is_impersonated, client_id, retention_class, details_sha256, details"

// joinedEvents is the statement shape that returns each matching event once,
// with its details: a security-class event's from its own row, a content-class
// event's from the details table, joined by id and hash. A content-class event
// past the content window has none.
func (s *Store) joinedEvents(p *sqlParams, pl plan, ordered bool) (string, bool) {
	events, ok := s.eventsWhere(p, pl)
	if !ok {
		return "", false
	}
	details := s.detailsWhere(p, pl)
	security := p.text(string(business.RetentionSecurity))
	statement := "SELECT e.deployment_id, e.event_id, e.org_id, e.actor_id, e.actor_type, e.event_type, e.schema_version, e.resource, " +
		"e.resource_id, e.occurred_at, e.ip_address, e.impersonated_by, e.is_impersonated, e.client_id, e.retention_class, e.details_sha256, " +
		"if(e.retention_class = " + security + ", e.details, d.details) AS payload " +
		"FROM (SELECT " + eventColumns + " FROM " + EventsTable + " WHERE " + events.String() + " LIMIT 1 BY event_id) AS e " +
		"LEFT JOIN (SELECT event_id, details_sha256, any(details) AS details FROM " + DetailsTable + " WHERE " + details.String() +
		" GROUP BY event_id, details_sha256) AS d ON e.event_id = d.event_id AND e.details_sha256 = d.details_sha256"
	if ordered {
		statement += " ORDER BY e.occurred_at DESC, e.event_id DESC"
	}
	return statement, true
}
