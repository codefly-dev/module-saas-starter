package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/analytics"
)

func (s *PostgresJobStore) RecordDelivery(
	ctx context.Context,
	record analytics.DeliveryRecord,
) error {
	if record.JobID == "" || record.CommandID == "" ||
		record.Kind == "" || record.ProviderReference == "" ||
		record.DeliveredAt.IsZero() {
		return errors.New("analytics: delivery record is incomplete")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO analytics_deliveries (
			job_id,
			command_id,
			kind,
			provider_reference,
			duplicate,
			delivered_at
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (job_id) DO UPDATE
		SET provider_reference = EXCLUDED.provider_reference,
		    duplicate = analytics_deliveries.duplicate OR EXCLUDED.duplicate,
		    delivered_at = GREATEST(
		        analytics_deliveries.delivered_at,
		        EXCLUDED.delivered_at
		    )`,
		record.JobID,
		record.CommandID,
		record.Kind,
		record.ProviderReference,
		record.Duplicate,
		record.DeliveredAt,
	)
	if err != nil {
		return fmt.Errorf("record analytics delivery: %w", err)
	}
	return nil
}

var _ analytics.DeliveryRecorder = (*PostgresJobStore)(nil)
