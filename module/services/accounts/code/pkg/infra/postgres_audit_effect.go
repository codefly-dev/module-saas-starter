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
	var priorFingerprint, eventID *string
	err = q.QueryRow(ctx, `SELECT request_fingerprint, audit_event_id::text
  FROM audit_event_idempotency WHERE org_id=$1 AND event_type=$2 AND idempotency_key=$3`,
		org, string(entry.EventType), entry.IdempotencyKey).Scan(&priorFingerprint, &eventID)
	if err != nil {
		return false, err
	}
	if priorFingerprint == nil || eventID == nil {
		return false, business.ErrAuditIdempotencyUnverifiable
	}
	if *priorFingerprint != fingerprint {
		return false, business.ErrAuditIdempotencyConflict
	}
	return false, nil
}
