package infra

import (
	"context"
	"fmt"

	"accounts/pkg/email"

	"github.com/google/uuid"
)

// RecordDeliveryEvent invokes the narrow SECURITY DEFINER projection installed
// by migration 99. The job-worker connection role has EXECUTE on that function
// but no direct authority over invitations or the event ledger. The event is
// already provider-neutral and carries a canonical delivery status; this layer
// only marshals it to the projection call.
func (s *PostgresJobStore) RecordDeliveryEvent(
	ctx context.Context,
	event email.DeliveryEvent,
) (bool, error) {
	var invitationID any
	if event.InvitationID != "" {
		parsed, err := uuid.Parse(event.InvitationID)
		if err == nil {
			invitationID = parsed
		}
	}
	var status any
	if event.Status != "" {
		status = string(event.Status)
	}
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		SELECT public.record_delivery_event(
			$1, $2, $3, $4, $5, $6, $7
		)`,
		event.Provider,
		event.EventID,
		event.ProviderMessageID,
		event.EventType,
		status,
		event.OccurredAt,
		invitationID,
	).Scan(&inserted)
	if err != nil {
		return false, fmt.Errorf("record delivery event: %w", err)
	}
	return inserted, nil
}

var _ email.DeliveryEventRecorder = (*PostgresJobStore)(nil)
