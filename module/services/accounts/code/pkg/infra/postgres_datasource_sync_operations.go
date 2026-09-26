package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"accounts/pkg/business"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/jackc/pgx/v5"
)

// datasourceSyncColumns reads one sync job's lifecycle. The delivery key is what
// its hand-off jobs carry as datasource.delivery_id: a reconcile hands off under
// its own job id, a webhook delivery under the provider's delivery id.
const datasourceSyncColumns = `
		SELECT id::text, topic, state, attempt_count, max_attempts,
		       created_at, last_attempt_at, completed_at, dead_lettered_at, available_at,
		       COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
		       COALESCE(attributes ->> 'datasource.reconcile_mode', ''),
		       COALESCE(attributes ->> 'datasource.delivery_id', id::text)
		FROM job_messages
		WHERE queue = $1
		  AND topic = ANY($2)
		  AND source = ANY($3)
		  AND attributes ->> 'datasource.org_id' = $4
		  AND attributes ->> 'datasource.source_id' = $5`

// GetDatasourceSyncOperation reads one sync of a source — the job named by
// jobID, or with jobID empty the source's latest — with the change set it handed
// to the consuming module's queue. Every predicate binds the job to the
// organization and source, so a job id from another source reads as not found.
func (s *PostgresJobStore) GetDatasourceSyncOperation(
	ctx context.Context,
	orgID string,
	sourceID string,
	jobID string,
) (*business.DatasourceSyncOperation, error) {
	args := []any{
		business.DatasourceDeliveryQueue,
		business.DatasourceSyncTopics,
		business.DatasourceSyncSources,
		orgID,
		sourceID,
	}
	query := datasourceSyncColumns
	if jobID == "" {
		query += `
		ORDER BY created_at DESC, enqueue_seq DESC
		LIMIT 1`
	} else {
		query += `
		  AND id = $6::uuid`
		args = append(args, jobID)
	}

	operation := &business.DatasourceSyncOperation{}
	record := &operation.Record
	var state, deliveryKey string
	err := s.pool.QueryRow(ctx, query, args...).Scan(
		&record.JobID, &record.Topic, &state, &record.Attempt, &record.MaxAttempts,
		&record.CreatedAt, &record.LastAttemptAt, &record.CompletedAt, &record.DeadAt, &record.AvailableAt,
		&record.ErrorCode, &record.ErrorMessage, &record.ReconcileMode, &deliveryKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.ErrDatasourceSyncNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get datasource sync: %w", err)
	}
	record.State, err = jobs.ParseDatabaseState(state)
	if err != nil {
		return nil, err
	}
	operation.JobID, operation.State = record.JobID, record.State

	if err := s.readDatasourceSyncHandoff(ctx, orgID, sourceID, deliveryKey, &operation.Handoff); err != nil {
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
		ORDER BY created_at, id`, deliveryKey, orgID, sourceID,
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

// readDatasourceSyncHandoff aggregates the change-set jobs a sync handed off, in
// one statement: an incremental change set is one job per file, so its size is
// bounded by the compiler's changed-file limit, never read row by row here.
func (s *PostgresJobStore) readDatasourceSyncHandoff(
	ctx context.Context,
	orgID, sourceID, deliveryKey string,
	handoff *business.DatasourceSyncHandoff,
) error {
	var firstAt, lastDoneAt, firstDeadAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE state = 'succeeded'),
		       count(*) FILTER (WHERE state IN ('dead_letter', 'canceled')),
		       min(created_at),
		       max(completed_at),
		       min(dead_lettered_at),
		       COALESCE(bool_or(topic = $7), false),
		       COALESCE(max(attributes ->> 'github.commit'), ''),
		       count(*) FILTER (WHERE topic = $8 AND attributes ->> 'github.change_type' = 'added'),
		       count(*) FILTER (WHERE topic = $8 AND attributes ->> 'github.change_type' IN ('modified', 'renamed')),
		       count(*) FILTER (WHERE topic = $8 AND attributes ->> 'github.change_type' = 'removed'),
		       COALESCE(max(attributes ->> 'datasource.changes.files') FILTER (WHERE topic = $7), ''),
		       COALESCE(max(attributes ->> 'datasource.changes.added') FILTER (WHERE topic = $7), ''),
		       COALESCE(max(attributes ->> 'datasource.changes.modified') FILTER (WHERE topic = $7), ''),
		       COALESCE(max(attributes ->> 'datasource.changes.deleted') FILTER (WHERE topic = $7), ''),
		       COALESCE(max(attributes ->> 'datasource.changes.split_known') FILTER (WHERE topic = $7), '')
		FROM job_messages
		WHERE attributes ->> 'datasource.delivery_id' = $1
		  AND attributes ->> 'datasource.org_id' = $2
		  AND attributes ->> 'datasource.source_id' = $3
		  AND queue = $4
		  AND topic = ANY($5)
		  AND source = $6`,
		deliveryKey, orgID, sourceID,
		business.DatasourceIngestQueue,
		business.DatasourceHandoffTopics,
		business.DatasourceSyncSource,
		business.DatasourceSnapshotTopic,
		business.DatasourceChangeSetFileTopic,
	).Scan(
		&handoff.Jobs, &handoff.Succeeded, &handoff.Dead,
		&firstAt, &lastDoneAt, &firstDeadAt,
		&handoff.Snapshot, &handoff.Commit,
		&handoff.Added, &handoff.Modified, &handoff.Deleted,
		&handoff.SnapshotFiles, &handoff.SnapshotAdded, &handoff.SnapshotModified, &handoff.SnapshotDeleted, &handoff.SnapshotSplitKnown,
	)
	if err != nil {
		return fmt.Errorf("aggregate datasource sync hand-off: %w", err)
	}
	handoff.FirstAt, handoff.LastDoneAt, handoff.FirstDeadAt = firstAt, lastDoneAt, firstDeadAt
	return nil
}
