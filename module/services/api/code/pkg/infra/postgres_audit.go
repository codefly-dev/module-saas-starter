package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"api/pkg/business"
	"api/pkg/gen"
)

func (s *PostgresStore) InsertAuditEvent(ctx context.Context, entry business.AuditEntry) error {
	q := s.getQueryExecutor(ctx)

	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		metadata = []byte("{}")
	}

	_, err = q.Exec(ctx, `
		INSERT INTO audit_events (actor_id, actor_type, action, resource, resource_id, org_id, metadata, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		nilIfNotUUID(entry.ActorID), entry.ActorType, entry.Action,
		entry.Resource, nilIfNotUUID(entry.ResourceID), nilIfNotUUID(entry.OrgID),
		metadata, nilIfEmpty(entry.IPAddress))
	return err
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

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	if pageSize == 0 {
		pageSize = 50
	}

	query := fmt.Sprintf(`SELECT id, actor_id, actor_type, action, resource, resource_id, org_id, metadata, ip_address, created_at
		FROM audit_events %s ORDER BY created_at DESC LIMIT $%d`, where, argN)
	args = append(args, pageSize)

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

	return events, "", int32(len(events)), nil
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
