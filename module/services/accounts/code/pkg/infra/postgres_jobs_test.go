package infra_test

import (
	"context"
	"testing"

	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

func TestJobWorkerPoolHasExactAuthority(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var currentRole string
	var bypassRLS, canSelectMessages, canInsertMessages, canUpdateMessages bool
	var canDeleteMessages, canInsertAttempts, canUpdateAttempts, canReadTransitions bool
	var canEnqueueMessages bool
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT current_user,
		       rolbypassrls,
		       has_table_privilege(current_user, 'job_messages', 'SELECT'),
		       has_table_privilege(current_user, 'job_messages', 'INSERT'),
		       has_table_privilege(current_user, 'job_messages', 'UPDATE'),
		       has_table_privilege(current_user, 'job_messages', 'DELETE'),
		       has_table_privilege(current_user, 'job_attempts', 'INSERT'),
		       has_table_privilege(current_user, 'job_attempts', 'UPDATE'),
		       has_table_privilege(current_user, 'job_state_transitions', 'SELECT'),
		       has_function_privilege(
		           current_user,
		           'public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamptz,bytea)',
		           'EXECUTE'
		       )
		FROM pg_roles
		WHERE rolname = current_user`,
	).Scan(
		&currentRole,
		&bypassRLS,
		&canSelectMessages,
		&canInsertMessages,
		&canUpdateMessages,
		&canDeleteMessages,
		&canInsertAttempts,
		&canUpdateAttempts,
		&canReadTransitions,
		&canEnqueueMessages,
	))
	require.Equal(t, "app_job_worker", currentRole)
	require.True(t, bypassRLS)
	require.True(t, canSelectMessages)
	require.True(t, canInsertMessages)
	require.True(t, canUpdateMessages)
	require.False(t, canDeleteMessages)
	require.True(t, canInsertAttempts)
	require.True(t, canUpdateAttempts)
	require.True(t, canReadTransitions)
	require.True(t, canEnqueueMessages)

	var forbiddenCount int
	err = pool.QueryRow(testCtx, `SELECT COUNT(*) FROM api_keys`).Scan(&forbiddenCount)
	require.Error(t, err, "generic workers must not inherit product-table authority")
}

func TestJobEnqueueFunctionHasExactProducerRoles(t *testing.T) {
	const signature = "public.enqueue_job_message(text,text,uuid,uuid,text,text,text,text,text,integer,bytea,text,jsonb,smallint,integer,timestamptz,bytea)"
	roles := map[string]bool{
		"app_tenant":         true,
		"app_control_plane":  true,
		"app_billing_worker": false,
		"app_webhook_worker": false,
		"app_job_worker":     true,
	}
	for role, want := range roles {
		var got bool
		require.NoError(t, testStore.Pool().QueryRow(testCtx, `
			SELECT has_function_privilege($1, $2, 'EXECUTE')`, role, signature,
		).Scan(&got), role)
		require.Equal(t, want, got, role)
	}
	var publicCanExecute bool
	require.NoError(t, testStore.Pool().QueryRow(testCtx, `
		SELECT COALESCE(bool_or(
			acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
		), false)
		FROM pg_proc proc
		CROSS JOIN LATERAL aclexplode(proc.proacl) acl
		WHERE proc.oid = $1::regprocedure`, signature,
	).Scan(&publicCanExecute))
	require.False(t, publicCanExecute)
}

func TestJobEnqueueRLSBindsTenantAndSubject(t *testing.T) {
	worker, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)

	orgA := uuid.New()
	orgB := uuid.New()
	tenantJob, err := enqueueTenantJob(testCtx, orgA, orgA)
	require.NoError(t, err)
	_, err = enqueueTenantJob(testCtx, orgA, orgB)
	require.Error(t, err)

	subjectA := uuid.New()
	subjectB := uuid.New()
	subjectJob, err := enqueueSubjectJob(testCtx, subjectA, subjectA)
	require.NoError(t, err)
	_, err = enqueueSubjectJob(testCtx, subjectA, subjectB)
	require.Error(t, err)

	err = testStore.WithOrgTx(testCtx, orgA.String(), func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, enqueueTestJob(
			jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			&jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			"test.global",
		))
		return err
	})
	require.Error(t, err, "request traffic must not enqueue global work")

	err = testStore.WithOrgTx(testCtx, orgA.String(), func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, enqueueTestJob(
			jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			&jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
				OrganizationId: orgA.String(),
			}},
			"test.inbox",
		))
		return err
	})
	require.Error(t, err, "request traffic emits outbox work; only workers ingest inbox work")

	var count int
	require.NoError(t, worker.QueryRow(testCtx, `
		SELECT COUNT(*) FROM job_messages WHERE id = ANY($1)`,
		[]uuid.UUID{tenantJob, subjectJob},
	).Scan(&count))
	require.Equal(t, 2, count)
}

func TestJobDatabaseStateMachineAndTransitionHistory(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	jobID := uuid.New()
	idempotencyKey := uuid.NewString()
	_, err = pool.Exec(testCtx, `
		INSERT INTO job_messages (
			id, direction, scope_kind, queue, topic, source,
			idempotency_key, request_fingerprint, ordering_key, payload
		) VALUES ($1, 'inbox', 'global', 'test.jobs', 'test.lifecycle',
		          'infra-test', $2, decode(repeat('00', 32), 'hex'), 'ordering-a', $3)`,
		jobID, idempotencyKey, []byte(`{"event":"created"}`),
	)
	require.NoError(t, err)

	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET state = 'succeeded', completed_at = NOW()
		WHERE id = $1`, jobID)
	require.Error(t, err, "pending jobs cannot skip processing")

	leaseOne := uuid.New()
	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET state = 'processing',
		    attempt_count = 1,
		    lease_owner = 'worker-a',
		    lease_token = $2,
		    lease_expires_at = NOW() + INTERVAL '1 minute',
		    heartbeat_at = NOW(),
		    last_attempt_at = NOW()
		WHERE id = $1`, jobID, leaseOne)
	require.NoError(t, err)

	attemptOne := uuid.New()
	_, err = pool.Exec(testCtx, `
		INSERT INTO job_attempts (
			id, job_id, attempt_number, worker_id, lease_token
		) VALUES ($1, $2, 1, 'worker-a', $3)`, attemptOne, jobID, leaseOne)
	require.NoError(t, err)

	_, err = pool.Exec(testCtx, `
		UPDATE job_messages SET payload = 'changed'::bytea WHERE id = $1`, jobID)
	require.Error(t, err, "job payload must remain immutable")

	_, err = pool.Exec(testCtx, `
		UPDATE job_attempts
		SET outcome = 'retryable_failure',
		    error_code = 'test.transient',
		    error_message = 'retry me',
		    finished_at = NOW()
		WHERE id = $1`, attemptOne)
	require.NoError(t, err)

	_, err = pool.Exec(testCtx, `
		UPDATE job_attempts SET error_message = 'rewritten' WHERE id = $1`, attemptOne)
	require.Error(t, err, "completed job-attempt history must be immutable")

	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET state = 'retrying',
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    heartbeat_at = NULL,
		    last_error_code = 'test.transient',
		    last_error_message = 'retry me',
		    available_at = NOW() + INTERVAL '1 second'
		WHERE id = $1`, jobID)
	require.NoError(t, err)

	leaseTwo := uuid.New()
	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET state = 'processing',
		    attempt_count = 2,
		    lease_owner = 'worker-b',
		    lease_token = $2,
		    lease_expires_at = NOW() + INTERVAL '1 minute',
		    heartbeat_at = NOW(),
		    last_attempt_at = NOW()
		WHERE id = $1`, jobID, leaseTwo)
	require.NoError(t, err)

	_, err = pool.Exec(testCtx, `
		UPDATE job_messages
		SET state = 'succeeded',
		    lease_owner = NULL,
		    lease_token = NULL,
		    lease_expires_at = NULL,
		    heartbeat_at = NULL,
		    completed_at = NOW()
		WHERE id = $1`, jobID)
	require.NoError(t, err)

	_, err = pool.Exec(testCtx, `
		UPDATE job_messages SET state = 'pending', completed_at = NULL WHERE id = $1`, jobID)
	require.Error(t, err, "terminal jobs must not be rewritten for replay")

	var state string
	var attemptCount int
	var stateVersion int64
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT state, attempt_count, state_version
		FROM job_messages
		WHERE id = $1`, jobID,
	).Scan(&state, &attemptCount, &stateVersion))
	wantState, err := jobs.DatabaseState(jobsv1.JobState_JOB_STATE_SUCCEEDED)
	require.NoError(t, err)
	require.Equal(t, wantState, state)
	require.Equal(t, 2, attemptCount)
	require.EqualValues(t, 4, stateVersion)

	rows, err := pool.Query(testCtx, `
		SELECT from_state, to_state, state_version, actor
		FROM job_state_transitions
		WHERE job_id = $1
		ORDER BY state_version`, jobID)
	require.NoError(t, err)
	defer rows.Close()

	type transition struct {
		from    *string
		to      string
		version int64
		actor   string
	}
	var transitions []transition
	for rows.Next() {
		var item transition
		require.NoError(t, rows.Scan(&item.from, &item.to, &item.version, &item.actor))
		transitions = append(transitions, item)
	}
	require.NoError(t, rows.Err())
	require.Len(t, transitions, 5)
	require.Nil(t, transitions[0].from)
	require.Equal(t, "pending", transitions[0].to)
	require.Equal(t, "succeeded", transitions[4].to)
	for index, item := range transitions {
		require.EqualValues(t, index, item.version)
		require.Equal(t, "app_job_worker", item.actor)
	}
}

func TestJobProtoAndDatabaseStateMachinesMatch(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	states := []jobsv1.JobState{
		jobsv1.JobState_JOB_STATE_PENDING,
		jobsv1.JobState_JOB_STATE_PROCESSING,
		jobsv1.JobState_JOB_STATE_RETRYING,
		jobsv1.JobState_JOB_STATE_SUCCEEDED,
		jobsv1.JobState_JOB_STATE_DEAD_LETTER,
		jobsv1.JobState_JOB_STATE_CANCELED,
	}
	for _, from := range states {
		fromDatabase, err := jobs.DatabaseState(from)
		require.NoError(t, err)
		for _, to := range states {
			toDatabase, err := jobs.DatabaseState(to)
			require.NoError(t, err)

			var databaseAllows bool
			require.NoError(t, pool.QueryRow(testCtx, `
				SELECT job_state_transition_is_valid($1, $2)`,
				fromDatabase, toDatabase,
			).Scan(&databaseAllows))
			require.Equal(
				t, jobs.CanTransition(from, to), databaseAllows,
				"protobuf/Go and PostgreSQL disagree for %s -> %s", from, to,
			)
		}
	}
}

func enqueueTenantJob(ctx context.Context, currentOrg, targetOrg uuid.UUID) (uuid.UUID, error) {
	var jobID uuid.UUID
	err := testStore.WithOrgTx(ctx, currentOrg.String(), func(ctx context.Context) error {
		response, err := testStore.EnqueueJob(ctx, enqueueTestJob(
			jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			&jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{
				OrganizationId: targetOrg.String(),
			}},
			"test.tenant",
		))
		if err == nil {
			jobID, err = uuid.Parse(response.GetJobId())
		}
		return err
	})
	return jobID, err
}

func enqueueSubjectJob(ctx context.Context, currentSubject, targetSubject uuid.UUID) (uuid.UUID, error) {
	var jobID uuid.UUID
	err := testStore.WithUserTx(ctx, currentSubject.String(), func(ctx context.Context) error {
		response, err := testStore.EnqueueJob(ctx, enqueueTestJob(
			jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			&jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{
				SubjectId: targetSubject.String(),
			}},
			"test.subject",
		))
		if err == nil {
			jobID, err = uuid.Parse(response.GetJobId())
		}
		return err
	})
	return jobID, err
}

func enqueueTestJob(
	direction jobsv1.JobDirection,
	scope *jobsv1.JobScope,
	topic string,
) *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction: direction, Scope: scope, Queue: "test.jobs", Topic: topic,
		Source: "infra-test", IdempotencyKey: uuid.NewString(), SchemaVersion: 1,
		Payload: []byte(`{}`), ContentType: "application/json", MaxAttempts: 4,
	}}
}
