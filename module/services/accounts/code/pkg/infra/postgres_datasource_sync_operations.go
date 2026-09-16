package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresJobStore) GetDatasourceSyncOperation(
	ctx context.Context,
	orgID string,
	sourceID string,
	jobID string,
) (*business.DatasourceSyncOperation, error) {
	operation := &business.DatasourceSyncOperation{JobID: jobID}
	var state string
	err := s.pool.QueryRow(ctx, `
		SELECT state
		FROM job_messages
		WHERE id = $1::uuid
		  AND queue = $2
		  AND topic = $3
		  AND source = $4
		  AND attributes ->> 'datasource.org_id' = $5
		  AND attributes ->> 'datasource.source_id' = $6`,
		jobID,
		business.DatasourceDeliveryQueue,
		business.DatasourceReconcileTopic,
		business.DatasourceReconcileSource,
		orgID,
		sourceID,
	).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.ErrDatasourceSyncNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get datasource sync: %w", err)
	}
	operation.State, err = jobs.ParseDatabaseState(state)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id::text, state,
		       COALESCE(execution_owner, ''),
		       COALESCE(execution_kind, ''),
		       COALESCE(execution_id, '')
		FROM job_messages
		WHERE attributes ->> 'datasource.delivery_id' = $1
		  AND attributes ->> 'datasource.org_id' = $2
		  AND attributes ->> 'datasource.source_id' = $3
		  AND queue = $4
		  AND topic = $5
		  AND source = $6
		ORDER BY created_at, id`, jobID, orgID, sourceID,
		business.DatasourceIngestQueue,
		business.DatasourceSnapshotTopic,
		business.DatasourceSyncSource,
	)
	if err != nil {
		return nil, fmt.Errorf("list datasource sync deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var delivery business.DatasourceSyncDelivery
		var state, owner, kind, id string
		if err := rows.Scan(&delivery.JobID, &state, &owner, &kind, &id); err != nil {
			return nil, fmt.Errorf("scan datasource sync delivery: %w", err)
		}
		delivery.State, err = jobs.ParseDatabaseState(state)
		if err != nil {
			return nil, err
		}
		if owner != "" {
			delivery.Execution = &jobsv1.JobExecutionReference{Owner: owner, Kind: kind, Id: id}
		}
		operation.Deliveries = append(operation.Deliveries, delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list datasource sync deliveries: %w", err)
	}
	return operation, nil
}
