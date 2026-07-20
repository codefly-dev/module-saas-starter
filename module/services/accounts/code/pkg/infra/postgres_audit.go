package infra

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

func (s *PostgresStore) InsertAuditEvent(ctx context.Context, entry business.AuditEntry) error {
	q := s.getQueryExecutor(ctx)
	if entry.ID == "" {
		entry.ID = business.NewIDString()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}

	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}

	_, err = q.Exec(ctx, `
		INSERT INTO audit_events (
			id, actor_id, actor_type, action, resource, resource_id,
			org_id, metadata, ip_address, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		entry.ID, nilIfNotUUID(entry.ActorID), entry.ActorType, entry.Action,
		entry.Resource, nilIfNotUUID(entry.ResourceID), nilIfNotUUID(entry.OrgID),
		metadata, nilIfEmpty(entry.IPAddress), entry.CreatedAt)
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

func (s *PostgresStore) QueryAuditLog(ctx context.Context, orgID, actorID, action, resource, resourceID string,
	from, to *time.Time, pageSize int32, pageToken string) ([]business.AuditEntry, string, int32, error) {
	q := s.getQueryExecutor(ctx)

	var conditions []string
	var args []any
	argN := 1

	if orgID != "" {
		conditions = append(conditions, fmt.Sprintf("org_id = $%d", argN))
		args = append(args, orgID)
		argN++
	}
	if actorID != "" {
		conditions = append(conditions, fmt.Sprintf("actor_id = $%d", argN))
		args = append(args, actorID)
		argN++
	}
	if action != "" {
		conditions = append(conditions, fmt.Sprintf("action = $%d", argN))
		args = append(args, action)
		argN++
	}
	if resource != "" {
		conditions = append(conditions, fmt.Sprintf("resource = $%d", argN))
		args = append(args, resource)
		argN++
	}
	if resourceID != "" {
		conditions = append(conditions, fmt.Sprintf("resource_id = $%d", argN))
		args = append(args, resourceID)
		argN++
	}
	if from != nil {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", argN))
		args = append(args, *from)
		argN++
	}
	if to != nil {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", argN))
		args = append(args, *to)
		argN++
	}

	if pageSize == 0 {
		pageSize = 50
	}

	// Apply the keyset cursor. The previous implementation ignored pageToken
	// entirely (and always returned an empty nextToken), so every audit export
	// was silently truncated to the first page. The token encodes the last
	// returned row's (created_at, id); the compound comparison is stable under
	// the DESC ordering even when many rows share a created_at.
	if pageToken != "" {
		ct, id, err := decodeAuditCursor(pageToken)
		if err != nil {
			return nil, "", 0, fmt.Errorf("invalid page token: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf("(created_at, id) < ($%d, $%d)", argN, argN+1))
		args = append(args, ct, id)
		argN += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Fetch one extra row to detect whether a further page exists.
	query := fmt.Sprintf(`SELECT id, actor_id, actor_type, action, resource, resource_id, org_id, metadata, ip_address, created_at
		FROM audit_events %s ORDER BY created_at DESC, id DESC LIMIT $%d`, where, argN)
	args = append(args, pageSize+1)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, err
	}
	defer rows.Close()

	var events []business.AuditEntry
	for rows.Next() {
		var e business.AuditEntry
		var metadataJSON []byte
		var actorID, resourceID, orgID, ipAddress *string

		err := rows.Scan(&e.ID, &actorID, &e.ActorType, &e.Action, &e.Resource,
			&resourceID, &orgID, &metadataJSON, &ipAddress, &e.CreatedAt)
		if err != nil {
			return nil, "", 0, err
		}
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

		var metadata map[string]string
		if json.Unmarshal(metadataJSON, &metadata) == nil {
			e.Metadata = metadata
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

// AuditEntryToProto converts a business AuditEntry to proto AuditEvent.
func AuditEntryToProto(e business.AuditEntry) *gen.AuditEvent {
	event := &gen.AuditEvent{
		Id:         e.ID,
		ActorId:    e.ActorID,
		ActorType:  e.ActorType,
		Action:     e.Action,
		Resource:   e.Resource,
		ResourceId: e.ResourceID,
		OrgId:      e.OrgID,
		IpAddress:  e.IPAddress,
		CreatedAt:  timestamppb.New(e.CreatedAt),
	}
	if e.Metadata != nil {
		event.Metadata = e.Metadata
	}
	return event
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
