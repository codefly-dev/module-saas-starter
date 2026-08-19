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

// AggregateAuditLog groups events for analytics. groupBy selects the key
// dimension (event_type, category, actor, or time); bucket sizes the time
// grain (day/week/month) when groupBy == "time". Ordering is deterministic:
// time buckets ascending, everything else by descending count.
func (s *PostgresStore) AggregateAuditLog(ctx context.Context, q business.AuditQuery, groupBy, bucket string) ([]business.AuditAggregateBucket, error) {
	exec := s.getQueryExecutor(ctx)
	where, args := auditWhere(q, 1)

	var keyExpr, orderBy string
	switch groupBy {
	case "category":
		keyExpr = "COALESCE((SELECT category FROM audit_event_types t WHERE t.name = audit_events.event_type), 'unknown')"
		orderBy = "count DESC"
	case "actor":
		keyExpr = "COALESCE(actor_id::text, '')"
		orderBy = "count DESC"
	case "time":
		grain := "day"
		switch bucket {
		case "week", "month", "day":
			grain = bucket
		}
		keyExpr = fmt.Sprintf("to_char(date_trunc('%s', created_at), 'YYYY-MM-DD\"T\"HH24:MI:SSOF')", grain)
		orderBy = "key ASC"
	default: // "event_type"
		keyExpr = "event_type"
		orderBy = "count DESC"
	}

	query := fmt.Sprintf(`SELECT %s AS key, COUNT(*) AS count FROM audit_events %s GROUP BY key ORDER BY %s`,
		keyExpr, where, orderBy)

	rows, err := exec.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []business.AuditAggregateBucket
	for rows.Next() {
		var b business.AuditAggregateBucket
		if err := rows.Scan(&b.Key, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
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
