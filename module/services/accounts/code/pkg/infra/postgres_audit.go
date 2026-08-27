package infra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/business"
)

func (s *PostgresStore) InsertAuditEvent(ctx context.Context, entry business.AuditEntry) error {
	q := s.getQueryExecutor(ctx)
	if entry.ID == "" {
		entry.ID = business.NewIDString()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	if entry.SchemaVersion == 0 {
		entry.SchemaVersion = 1
	}

	payload, err := json.Marshal(entry.Payload)
	if err != nil || entry.Payload == nil {
		payload = []byte("{}")
	}

	_, err = q.Exec(ctx, `
		INSERT INTO audit_events (
			id, event_type, schema_version, actor_id, actor_type,
			resource, resource_id, org_id, payload, ip_address, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		entry.ID, string(entry.EventType), entry.SchemaVersion,
		nilIfNotUUID(entry.ActorID), entry.ActorType,
		entry.Resource, nilIfNotUUID(entry.ResourceID), nilIfNotUUID(entry.OrgID),
		payload, nilIfEmpty(entry.IPAddress), entry.CreatedAt)
	return err
}

// encodeAuditCursor / decodeAuditCursor carry the keyset position for audit
// pagination. The token is opaque to callers: base64 of "<RFC3339Nano>|<id>".
func encodeAuditCursor(ct time.Time, id string) string {
	raw := ct.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAuditCursor(token string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	ct, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return ct, parts[1], nil
}

// auditWhere builds the shared WHERE clause for the search and aggregate paths
// from an AuditQuery, returning the SQL fragment and its ordered args.
func auditWhere(q business.AuditQuery, startArg int) (string, []any) {
	var conditions []string
	var args []any
	argN := startArg

	add := func(cond string, val any) {
		conditions = append(conditions, fmt.Sprintf(cond, argN))
		args = append(args, val)
		argN++
	}
	if q.OrgID != "" {
		add("org_id = $%d", q.OrgID)
	}
	if q.ActorID != "" {
		add("actor_id = $%d", q.ActorID)
	}
	if q.EventType != "" {
		add("event_type = $%d", q.EventType)
	}
	if q.Category != "" {
		add("event_type IN (SELECT name FROM audit_event_types WHERE category = $%d)", q.Category)
	}
	if q.Resource != "" {
		add("resource = $%d", q.Resource)
	}
	if q.ResourceID != "" {
		add("resource_id = $%d", q.ResourceID)
	}
	if len(q.PayloadContains) > 0 {
		if raw, err := json.Marshal(q.PayloadContains); err == nil {
			add("payload @> $%d::jsonb", string(raw))
		}
	}
	if q.From != nil {
		add("created_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("created_at <= $%d", *q.To)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	return where, args
}

func (s *PostgresStore) QueryAuditLog(ctx context.Context, q business.AuditQuery) ([]business.AuditEntry, string, int32, error) {
	exec := s.getQueryExecutor(ctx)

	where, args := auditWhere(q, 1)
	argN := len(args) + 1

	pageSize := q.PageSize
	if pageSize == 0 {
		pageSize = 50
	}

	// Apply the keyset cursor. The token encodes the last returned row's
	// (created_at, id); the compound comparison is stable under the DESC
	// ordering even when many rows share a created_at.
	if q.PageToken != "" {
		ct, id, err := decodeAuditCursor(q.PageToken)
		if err != nil {
			return nil, "", 0, fmt.Errorf("invalid page token: %w", err)
		}
		clause := fmt.Sprintf("(created_at, id) < ($%d, $%d)", argN, argN+1)
		if where == "" {
			where = "WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, ct, id)
		argN += 2
	}

	// Fetch one extra row to detect whether a further page exists.
	query := fmt.Sprintf(`SELECT id, event_type, schema_version, actor_id, actor_type, resource, resource_id, org_id, payload, ip_address, created_at
		FROM audit_events %s ORDER BY created_at DESC, id DESC LIMIT $%d`, where, argN)
	args = append(args, pageSize+1)

	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()

	var events []business.AuditEntry
	for rows.Next() {
		var e business.AuditEntry
		var eventType string
		var payloadJSON []byte
		var actorID, resourceID, orgID, ipAddress *string

		err := rows.Scan(&e.ID, &eventType, &e.SchemaVersion, &actorID, &e.ActorType, &e.Resource,
			&resourceID, &orgID, &payloadJSON, &ipAddress, &e.CreatedAt)
		if err != nil {
			return nil, "", 0, err
		}
		e.EventType = business.EventType(eventType)
		if actorID != nil {
			e.ActorID = *actorID
		}
		if resourceID != nil {
			e.ResourceID = *resourceID
		}
		if orgID != nil {
			e.OrgID = *orgID
		}
		if ipAddress != nil {
			e.IPAddress = *ipAddress
		}
		var payload map[string]any
		if json.Unmarshal(payloadJSON, &payload) == nil {
			e.Payload = payload
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, err
	}

	// If we fetched the extra row, there is a next page: trim it and emit a
	// cursor pointing at the last RETURNED row.
	nextToken := ""
	if len(events) > int(pageSize) {
		events = events[:pageSize]
		last := events[len(events)-1]
		nextToken = encodeAuditCursor(last.CreatedAt, last.ID)
	}

	return events, nextToken, int32(len(events)), nil
}

// AggregateAuditLog groups events for analytics per the spec: one or more group
// dimensions (event_type, category, actor, time, or a payload field), plus
// count/distinct-count/sum/avg/min/max/percentile metrics over payload fields
// and derived ratios. Ordering is deterministic: a sole time dimension sorts
// ascending; otherwise groups sort by descending count then by key. The caller
// (Service) has already validated the spec; the builder still guards its own
// switches so an unhandled shape fails loud rather than emitting wrong SQL.
func (s *PostgresStore) AggregateAuditLog(ctx context.Context, q business.AuditQuery, spec business.AuditAggregationSpec) ([]business.AuditAggregateBucket, error) {
	exec := s.getQueryExecutor(ctx)

	aq, err := buildAggregateQuery(q, spec)
	if err != nil {
		return nil, err
	}

	rows, err := exec.Query(ctx, aq.sql, aq.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []business.AuditAggregateBucket
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		// Column layout is [dim0..dimN, cnt, metric0..metricM].
		b := business.AuditAggregateBucket{
			Keys:    make([]string, len(aq.dims)),
			Metrics: make(map[string]float64, len(aq.aliases)+len(spec.Derived)),
		}
		for i := range aq.dims {
			b.Keys[i], _ = vals[i].(string)
		}
		b.Key = b.Keys[0]
		if c, ok := vals[len(aq.dims)].(int64); ok {
			b.Count = c
		}
		for i, alias := range aq.aliases {
			// A NULL aggregate (min/avg/max/percentile over zero numeric rows)
			// means "no data" — leave the alias absent rather than reporting 0.
			if v := vals[len(aq.dims)+1+i]; v != nil {
				b.Metrics[alias] = toFloat(v)
			}
		}
		for _, d := range spec.Derived {
			// A ratio is undefined when either operand is absent (its metric had
			// no data for this group) or the denominator is 0 — omit the alias in
			// those cases. Emitting 0 would be indistinguishable from a real 0
			// ratio, the same trap the NULL-aggregate handling above avoids.
			num, numOK := b.Metrics[d.Numerator]
			den, denOK := b.Metrics[d.Denominator]
			if numOK && denOK && den != 0 {
				b.Metrics[d.Alias] = num / den
			}
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// aggregateQuery is a rendered aggregation: the SQL, its ordered bind args, the
// effective group dimensions, and the metric aliases in column order — the last
// two let the caller map each result row back to keys and named metrics.
type aggregateQuery struct {
	sql     string
	args    []any
	dims    []string
	aliases []string
}

// buildAggregateQuery renders the aggregation SQL. Every caller-supplied value
// (payload keys, percentiles) is bound as a parameter, never interpolated, so
// the query is injection-safe.
func buildAggregateQuery(q business.AuditQuery, spec business.AuditAggregationSpec) (aggregateQuery, error) {
	where, args := auditWhere(q, 1)
	argN := len(args) + 1
	addArg := func(v any) int {
		args = append(args, v)
		n := argN
		argN++
		return n
	}

	dims := spec.GroupBy
	if len(dims) == 0 {
		dims = []string{"event_type"}
	}

	selectParts := make([]string, 0, len(dims)+1+len(spec.Metrics))
	groupCols := make([]string, 0, len(dims))
	orderDims := make([]string, 0, len(dims))
	soleTime := len(dims) == 1 && dims[0] == "time"
	for i, d := range dims {
		expr, err := auditDimensionExpr(d, spec.Bucket, addArg)
		if err != nil {
			return aggregateQuery{}, err
		}
		col := fmt.Sprintf("d%d", i)
		selectParts = append(selectParts, expr+" AS "+col)
		groupCols = append(groupCols, col)
		orderDims = append(orderDims, col+" ASC")
	}

	// COUNT(*) is always column "cnt": it backs the Count field and the ordering.
	selectParts = append(selectParts, "COUNT(*) AS cnt")

	aliases := make([]string, len(spec.Metrics))
	for i, m := range spec.Metrics {
		expr, err := auditMetricExpr(m, addArg)
		if err != nil {
			return aggregateQuery{}, err
		}
		selectParts = append(selectParts, fmt.Sprintf("%s AS m%d", expr, i))
		aliases[i] = m.ResolvedAlias()
	}

	orderBy := "cnt DESC, " + strings.Join(orderDims, ", ")
	if soleTime {
		orderBy = "d0 ASC"
	}

	sql := fmt.Sprintf("SELECT %s FROM audit_events %s GROUP BY %s ORDER BY %s",
		strings.Join(selectParts, ", "), where, strings.Join(groupCols, ", "), orderBy)
	return aggregateQuery{sql: sql, args: args, dims: dims, aliases: aliases}, nil
}

// auditDimensionExpr renders the group-key expression for one dimension. All
// exprs evaluate to text so result keys scan uniformly as strings.
func auditDimensionExpr(dim, bucket string, addArg func(any) int) (string, error) {
	if key, ok := strings.CutPrefix(dim, "payload:"); ok && key != "" {
		return fmt.Sprintf("COALESCE(payload ->> $%d, '')", addArg(key)), nil
	}
	switch dim {
	case "", "event_type":
		return "event_type", nil
	case "category":
		return "COALESCE((SELECT category FROM audit_event_types t WHERE t.name = audit_events.event_type), 'unknown')", nil
	case "actor":
		return "COALESCE(actor_id::text, '')", nil
	case "time":
		grain := "day"
		switch bucket {
		case "week", "month", "day":
			grain = bucket
		}
		return fmt.Sprintf("to_char(date_trunc('%s', created_at), 'YYYY-MM-DD\"T\"HH24:MI:SSOF')", grain), nil
	default:
		return "", fmt.Errorf("audit: unhandled group dimension %q", dim)
	}
}

// auditMetricExpr renders one metric's SQL aggregate.
func auditMetricExpr(m business.AuditMetric, addArg func(any) int) (string, error) {
	switch m.Op {
	case "", "count":
		return "COUNT(*)", nil
	case "count_distinct":
		expr, err := auditValueExpr(m.Field, addArg)
		if err != nil {
			return "", err
		}
		return "COUNT(DISTINCT " + expr + ")", nil
	case "sum":
		// An empty sum is 0 (additive identity); the others are undefined over
		// zero numeric rows and stay NULL so scanning omits them — coercing them
		// to 0 would be indistinguishable from a real 0 datum.
		return "COALESCE(SUM(" + auditNumericExpr(m.Field, addArg) + "), 0)", nil
	case "avg":
		return "AVG(" + auditNumericExpr(m.Field, addArg) + ")", nil
	case "min":
		return "MIN(" + auditNumericExpr(m.Field, addArg) + ")", nil
	case "max":
		return "MAX(" + auditNumericExpr(m.Field, addArg) + ")", nil
	case "percentile":
		pArg := addArg(m.Percentile)
		return fmt.Sprintf("percentile_cont($%d) WITHIN GROUP (ORDER BY %s)", pArg, auditNumericExpr(m.Field, addArg)), nil
	default:
		return "", fmt.Errorf("audit: unhandled metric op %q", m.Op)
	}
}

// auditValueExpr renders a text-valued expression for count_distinct: a known
// column, or a bound payload key.
func auditValueExpr(field string, addArg func(any) int) (string, error) {
	if key, ok := strings.CutPrefix(field, "payload:"); ok && key != "" {
		return fmt.Sprintf("payload ->> $%d", addArg(key)), nil
	}
	switch field {
	case "actor_id", "resource_id":
		return field + "::text", nil
	case "event_type", "resource":
		return field, nil
	case "category":
		return "(SELECT category FROM audit_event_types t WHERE t.name = audit_events.event_type)", nil
	default:
		return "", fmt.Errorf("audit: unhandled count_distinct field %q", field)
	}
}

// auditNumericExpr renders a payload key as a double, yielding NULL for missing
// or non-numeric values so aggregates skip them. It keys off the JSON type: a
// JSON number is taken directly (covering every numeric magnitude and format,
// not just the decimals a text regex happens to match), and a JSON string is
// taken only when it parses as a plain number. The key is bound, never
// interpolated; the caller has validated that field is a payload:<key>.
func auditNumericExpr(field string, addArg func(any) int) string {
	key := strings.TrimPrefix(field, "payload:")
	n := addArg(key)
	return fmt.Sprintf(`CASE
		WHEN jsonb_typeof(payload -> $%d) = 'number' THEN (payload ->> $%d)::double precision
		WHEN jsonb_typeof(payload -> $%d) = 'string' AND payload ->> $%d ~ '^-?[0-9]+(\.[0-9]+)?$' THEN (payload ->> $%d)::double precision
	END`, n, n, n, n, n)
}

// toFloat coerces a pgx-scanned aggregate value to float64. Counts arrive as
// int64; numeric aggregates as float64; empty groups as nil.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return 0
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nilIfNotUUID returns nil when s is not a valid UUID, else a pointer to
// s. The audit_events.actor_id column is typed UUID (nullable); system
// or fixture-seed actors don't have a real user id. Feeding postgres a
// non-UUID string fails with 22P02 "invalid input syntax for type uuid";
// treating them as nil preserves the audit entry but records the actor
// via actor_type alone.
func nilIfNotUUID(s string) *string {
	if s == "" {
		return nil
	}
	// UUID v4 string length is always 36 with 4 hyphens in fixed
	// positions. A cheap shape check avoids importing uuid just here.
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil
	}
	return &s
}
