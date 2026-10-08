package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
	"github.com/jackc/pgx/v5"
)

// ReserveAuditEffect is an owner-side transactional reservation. A duplicate
// INSERT waits for its competitor's commit/rollback; the separate SELECT then
// reads the committed binding under the store's READ COMMITTED transaction.
// Tenant RLS and the explicit key constrain both statements.
func (s *PostgresStore) ReserveAuditEffect(ctx context.Context, entry business.AuditEntry) (bool, error) {
	if entry.ID == "" || entry.IdempotencyKey == "" {
		return false, fmt.Errorf("audit effect requires an event identity and key")
	}
	fingerprint, err := business.AuditEffectFingerprint(entry)
	if err != nil {
		return false, err
	}
	org := entry.OrgID
	if org == "" {
		org = auditIdempotencySystemOrg
	}
	q := s.getQueryExecutor(ctx)
	var inserted bool
	err = q.QueryRow(ctx, `INSERT INTO audit_event_idempotency
  (org_id, event_type, idempotency_key, request_fingerprint, audit_event_id)
  VALUES ($1, $2, $3, $4, $5)
  ON CONFLICT (org_id, event_type, idempotency_key) DO NOTHING
  RETURNING true`, org, string(entry.EventType), entry.IdempotencyKey, fingerprint, entry.ID).Scan(&inserted)
	if err == nil {
		return inserted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	eventID, err := s.LookupAuditEffect(ctx, entry)
	if err != nil {
		return false, err
	}
	if eventID == "" {
		return false, fmt.Errorf("conflicting audit reservation is not visible")
	}
	return false, nil
}

// LookupAuditEffect never inserts or changes a reservation.
func (s *PostgresStore) LookupAuditEffect(ctx context.Context, entry business.AuditEntry) (string, error) {
	if entry.IdempotencyKey == "" {
		return "", fmt.Errorf("audit receipt requires a key")
	}
	fingerprint, err := business.AuditEffectFingerprint(entry)
	if err != nil {
		return "", err
	}
	org := entry.OrgID
	if org == "" {
		org = auditIdempotencySystemOrg
	}
	var priorFingerprint, eventID *string
	err = s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT request_fingerprint, audit_event_id::text
  FROM audit_event_idempotency WHERE org_id=$1 AND event_type=$2 AND idempotency_key=$3`,
		org, string(entry.EventType), entry.IdempotencyKey).Scan(&priorFingerprint, &eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if priorFingerprint == nil || eventID == nil {
		return "", business.ErrAuditIdempotencyUnverifiable
	}
	if *priorFingerprint != fingerprint {
		return "", business.ErrAuditIdempotencyConflict
	}
	return *eventID, nil
}
