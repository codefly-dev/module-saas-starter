package infra

import (
	"context"
	"fmt"

	"accounts/pkg/email"

	"github.com/google/uuid"
)

// RecordResendEvent invokes the narrow SECURITY DEFINER projection installed
// by migration 84. The job-worker connection role has EXECUTE on that function
// but no direct authority over invitations or the event ledger.
func (s *PostgresJobStore) RecordResendEvent(
	ctx context.Context,
	event email.ResendEvent,
) (bool, error) {
	var invitationID any
	if event.InvitationID != "" {
		parsed, err := uuid.Parse(event.InvitationID)
		if err == nil {
			invitationID = parsed
		}
	}
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		SELECT public.record_resend_delivery_event(
			$1, $2, $3, $4, $5
		)`,
		event.SvixID,
		event.Type,
		event.ProviderEmailID,
		event.CreatedAt,
		invitationID,
	).Scan(&inserted)
	if err != nil {
		return false, fmt.Errorf("record Resend delivery event: %w", err)
	}
	return inserted, nil
}

var _ email.ResendEventRecorder = (*PostgresJobStore)(nil)
