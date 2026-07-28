package infra_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"accounts/pkg/business"
	"accounts/pkg/email"
	accountsv1 "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	notificationsv1 "accounts/pkg/gen/saas/notifications/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPostgresJobProducerRequiresTransactionAndResolvesIdempotency(t *testing.T) {
	worker, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)

	orgID := uuid.NewString()
	request := producerRequest(tenantJobScope(orgID), jobsv1.JobDirection_JOB_DIRECTION_OUTBOX)
	_, err = testStore.EnqueueJob(testCtx, request)
	require.ErrorIs(t, err, jobs.ErrTransactionRequired)

	var inserted *jobsv1.EnqueueJobResponse
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		inserted, err = testStore.EnqueueJob(ctx, request)
		return err
	}))
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED, inserted.GetDisposition())

	var duplicate *jobsv1.EnqueueJobResponse
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		duplicate, err = testStore.EnqueueJob(ctx, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
		return err
	}))
	require.Equal(t, inserted.GetJobId(), duplicate.GetJobId())
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE, duplicate.GetDisposition())

	conflict := proto.Clone(request).(*jobsv1.EnqueueJobRequest)
	conflict.Job.Payload = []byte(`{"changed":true}`)
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, conflict)
		return err
	})
	require.ErrorIs(t, err, jobs.ErrIdempotencyConflict)

	wantOrderingKey, err := jobs.CanonicalOrderingKey(request.GetJob().GetOrdering())
	require.NoError(t, err)
	var count, fingerprintBytes int
	var orderingKey string
	require.NoError(t, worker.QueryRow(testCtx, `
		SELECT COUNT(*), MAX(octet_length(request_fingerprint)), MAX(ordering_key)
		FROM job_messages
		WHERE idempotency_key = $1`, request.GetJob().GetIdempotencyKey(),
	).Scan(&count, &fingerprintBytes, &orderingKey))
	require.Equal(t, 1, count)
	require.Equal(t, 32, fingerprintBytes)
	require.Equal(t, wantOrderingKey, orderingKey)
}

func TestPostgresJobProducerEnforcesRequestScopeAndDirection(t *testing.T) {
	orgID := uuid.NewString()
	otherOrgID := uuid.NewString()
	tests := []struct {
		name    string
		request *jobsv1.EnqueueJobRequest
	}{
		{
			name: "different tenant",
			request: producerRequest(
				tenantJobScope(otherOrgID), jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			),
		},
		{
			name: "global",
			request: producerRequest(
				&jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
				jobsv1.JobDirection_JOB_DIRECTION_OUTBOX,
			),
		},
		{
			name: "inbox",
			request: producerRequest(
				tenantJobScope(orgID), jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				_, err := testStore.EnqueueJob(ctx, test.request)
				return err
			})
			require.Error(t, err)
		})
	}

	subjectID := uuid.NewString()
	request := producerRequest(subjectJobScope(subjectID), jobsv1.JobDirection_JOB_DIRECTION_OUTBOX)
	require.NoError(t, testStore.WithUserTx(testCtx, subjectID, func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, request)
		return err
	}))
	err := testStore.WithUserTx(testCtx, uuid.NewString(), func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
		return err
	})
	require.Error(t, err)
}

func TestPostgresJobProducerRollsBackWithBusinessTransaction(t *testing.T) {
	worker, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)

	orgID := uuid.NewString()
	request := producerRequest(tenantJobScope(orgID), jobsv1.JobDirection_JOB_DIRECTION_OUTBOX)
	rollback := errors.New("rollback business mutation")
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		response, err := testStore.EnqueueJob(ctx, request)
		require.NoError(t, err)
		require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED, response.GetDisposition())
		return rollback
	})
	require.ErrorIs(t, err, rollback)

	var count int
	require.NoError(t, worker.QueryRow(testCtx, `
		SELECT COUNT(*) FROM job_messages WHERE idempotency_key = $1`,
		request.GetJob().GetIdempotencyKey(),
	).Scan(&count))
	require.Zero(t, count)
}

func TestPostgresJobProducerSerializesConcurrentExactRetries(t *testing.T) {
	orgID := uuid.NewString()
	request := producerRequest(tenantJobScope(orgID), jobsv1.JobDirection_JOB_DIRECTION_OUTBOX)

	start := make(chan struct{})
	responses := make(chan *jobsv1.EnqueueJobResponse, 2)
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			err := testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
				response, err := testStore.EnqueueJob(ctx, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
				if err == nil {
					responses <- response
				}
				return err
			})
			if err != nil {
				errorsFound <- err
			}
		}()
	}
	close(start)
	group.Wait()
	close(responses)
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	var jobID string
	dispositions := map[jobsv1.JobEnqueueDisposition]int{}
	for response := range responses {
		if jobID == "" {
			jobID = response.GetJobId()
		}
		require.Equal(t, jobID, response.GetJobId())
		dispositions[response.GetDisposition()]++
	}
	require.Equal(t, 1, dispositions[jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED])
	require.Equal(t, 1, dispositions[jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE])
}

func TestPostgresJobProducerAllowsPrivilegedInboxAndRejectsDirectTenantInsert(t *testing.T) {
	worker, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)
	producer := infra.NewPostgresJobStore(worker)

	request := producerRequest(
		&jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
		jobsv1.JobDirection_JOB_DIRECTION_INBOX,
	)
	inserted, err := producer.EnqueueJob(testCtx, request)
	require.NoError(t, err)
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED, inserted.GetDisposition())
	duplicate, err := producer.EnqueueJob(testCtx, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
	require.NoError(t, err)
	require.Equal(t, inserted.GetJobId(), duplicate.GetJobId())
	require.Equal(t, jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE, duplicate.GetDisposition())

	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		_, err := testStore.EnqueueJob(ctx, proto.Clone(request).(*jobsv1.EnqueueJobRequest))
		return err
	})
	require.Error(t, err, "control-plane traffic cannot enqueue inbox work")

	orgID := uuid.NewString()
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // test proves table authority
		_, err := tx.Exec(ctx, `
			INSERT INTO job_messages (
				direction, scope_kind, organization_id, queue, topic, source,
				idempotency_key, request_fingerprint, payload
			) VALUES (
				'outbox', 'tenant', $1, 'test.jobs', 'test.direct', 'infra-test',
				$2, decode(repeat('00', 32), 'hex'), '{}'::bytea
			)`, orgID, uuid.NewString())
		return err
	})
	require.Error(t, err, "request traffic receives function execution, not raw table INSERT")
}

func TestPostgresEmailOutboxEnforcesTenantAndPreAuthTransactionScopes(t *testing.T) {
	worker, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(worker.Close)
	outbox, err := email.NewOutbox(testStore, nil, "no-reply@example.com")
	require.NoError(t, err)

	orgID := uuid.NewString()
	tenantKey := uuid.NewString()
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		return outbox.Enqueue(ctx, tenantKey, email.TenantScope(orgID),
			"saas.accounts.invitations", testEmailMessage())
	}))

	preAuthKey := uuid.NewString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return outbox.Enqueue(ctx, preAuthKey, email.GlobalScope(),
			"saas.accounts.authentication", testEmailMessage())
	}))

	err = testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return outbox.Enqueue(ctx, uuid.NewString(), email.TenantScope(orgID),
			"saas.accounts.authentication", testEmailMessage())
	})
	require.Error(t, err, "pre-auth authority must not spoof tenant scope")

	err = outbox.Enqueue(testCtx, uuid.NewString(), email.GlobalScope(),
		"saas.accounts.authentication", testEmailMessage())
	require.ErrorIs(t, err, jobs.ErrTransactionRequired)

	rollbackKey := uuid.NewString()
	rollback := errors.New("rollback email producer")
	err = testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		require.NoError(t, outbox.Enqueue(ctx, rollbackKey, email.TenantScope(orgID),
			"saas.accounts.invitations", testEmailMessage()))
		return rollback
	})
	require.ErrorIs(t, err, rollback)

	var committed, rolledBack int
	require.NoError(t, worker.QueryRow(testCtx, `
		SELECT COUNT(*) FROM job_messages WHERE idempotency_key = ANY($1)`,
		[]string{tenantKey, preAuthKey},
	).Scan(&committed))
	require.Equal(t, 2, committed)
	require.NoError(t, worker.QueryRow(testCtx, `
		SELECT COUNT(*) FROM job_messages WHERE idempotency_key = $1`, rollbackKey,
	).Scan(&rolledBack))
	require.Zero(t, rolledBack)
}

func TestMagicLinkRunsThroughTransactionalGenericEmailWorker(t *testing.T) {
	workerPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(workerPool.Close)
	jobStore := infra.NewPostgresJobStore(workerPool)

	outbox, err := email.NewOutbox(
		testStore,
		infra.NewPostgresTemplateStore(testStore),
		"SaaS Starter <no-reply@example.com>",
	)
	require.NoError(t, err)
	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetEmailOutbox(outbox, "https://app.example.com")
	require.NoError(t, service.SendMagicLink(testCtx, "magic@example.com"))

	var jobID, state, orderingKey string
	var encoded []byte
	require.NoError(t, workerPool.QueryRow(testCtx, `
		SELECT id::text, state, ordering_key, payload
		FROM job_messages
		WHERE queue = $1 AND source = 'saas.accounts.authentication'
		ORDER BY created_at DESC
		LIMIT 1`, email.DeliveryQueue,
	).Scan(&jobID, &state, &orderingKey, &encoded))
	require.Equal(t, "pending", state)
	require.NotContains(t, orderingKey, "magic@example.com")
	payload := &notificationsv1.EmailDeliveryJob{}
	require.NoError(t, proto.Unmarshal(encoded, payload))
	require.Equal(t, []string{"magic@example.com"}, payload.GetTo())
	require.True(t, strings.Contains(payload.GetHtmlBody(),
		"https://app.example.com/auth/magic-link?token="))

	sender := email.NewFakeSender()
	handler, err := email.NewJobHandler(sender)
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: email.DeliveryQueue, Handler: handler,
		WorkerID: "email-integration-test", BatchSize: 100,
	})
	require.NoError(t, err)
	processed, err := worker.RunOnce(testCtx)
	require.NoError(t, err)
	require.Positive(t, processed)
	require.Contains(t, sender.ToAddresses(), "magic@example.com")

	require.NoError(t, workerPool.QueryRow(testCtx,
		`SELECT state FROM job_messages WHERE id = $1`, jobID,
	).Scan(&state))
	require.Equal(t, "succeeded", state)
}

func TestInvitationAndEmailJobCommitOrRollBackTogether(t *testing.T) {
	ownerID := seedUser(t)
	orgID := seedOrg(t, ownerID)
	templateStore := infra.NewPostgresTemplateStore(testStore)

	rejectingOutbox, err := email.NewOutbox(
		rejectingWebhookJobProducer{}, templateStore, "no-reply@example.com",
	)
	require.NoError(t, err)
	rejectingService, err := business.NewService(testStore)
	require.NoError(t, err)
	rejectingService.SetEntitlementChecker(business.NewDefaultEntitlementChecker(testStore))
	rejectingService.SetEmailOutbox(rejectingOutbox, "https://app.example.com")
	_, err = rejectingService.CreateInvitation(testCtx, ownerID, &accountsv1.CreateInvitationRequest{
		OrgId: orgID, Email: "rollback-invite@example.com", Role: accountsv1.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.ErrorContains(t, err, "reject generated webhook job")
	require.NoError(t, testStore.WithOrgTx(testCtx, orgID, func(ctx context.Context) error {
		invitations, err := testStore.ListInvitations(ctx, orgID, "")
		require.NoError(t, err)
		for _, invitation := range invitations {
			require.NotEqual(t, "rollback-invite@example.com", invitation.Email)
		}
		return nil
	}))

	outbox, err := email.NewOutbox(testStore, templateStore, "no-reply@example.com")
	require.NoError(t, err)
	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(testStore))
	service.SetEmailOutbox(outbox, "https://app.example.com")
	response, err := service.CreateInvitation(testCtx, ownerID, &accountsv1.CreateInvitationRequest{
		OrgId: orgID, Email: "queued-invite@example.com", Role: accountsv1.InvitationRole_INVITATION_ROLE_MEMBER,
	})
	require.NoError(t, err)
	require.NotNil(t, response.GetInvitation())

	workerPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(workerPool.Close)
	var scopeKind, jobOrgID string
	var encoded []byte
	require.NoError(t, workerPool.QueryRow(testCtx, `
		SELECT scope_kind, organization_id::text, payload
		FROM job_messages
		WHERE queue = $1 AND idempotency_key = $2`,
		email.DeliveryQueue, response.GetInvitation().GetId(),
	).Scan(&scopeKind, &jobOrgID, &encoded))
	require.Equal(t, "tenant", scopeKind)
	require.Equal(t, orgID, jobOrgID)
	payload := &notificationsv1.EmailDeliveryJob{}
	require.NoError(t, proto.Unmarshal(encoded, payload))
	require.Equal(t, []string{"queued-invite@example.com"}, payload.GetTo())
	require.Contains(t, payload.GetHtmlBody(), "/invitations/accept?token=")
}

func testEmailMessage() *email.Message {
	return &email.Message{
		From: "no-reply@example.com", To: []string{"user@example.com"},
		Subject: "Account notification", TextBody: "Notification body",
	}
}

func producerRequest(
	scope *jobsv1.JobScope,
	direction jobsv1.JobDirection,
) *jobsv1.EnqueueJobRequest {
	return &jobsv1.EnqueueJobRequest{Job: &jobsv1.NewJob{
		Direction:      direction,
		Scope:          scope,
		Queue:          "test.producer." + uuid.NewString(),
		Topic:          "test.producer.v1",
		Source:         "infra-test",
		IdempotencyKey: uuid.NewString(),
		Ordering: &jobsv1.JobOrderingKey{
			Namespace: "resource", Components: []string{"tenant/a", "object.1"},
		},
		SchemaVersion: 1,
		Payload:       []byte(`{"event":"created"}`),
		ContentType:   "application/json",
		Attributes:    map[string]string{"trace_id": "trace-1"},
		MaxAttempts:   4,
	}}
}

func tenantJobScope(orgID string) *jobsv1.JobScope {
	return &jobsv1.JobScope{Value: &jobsv1.JobScope_OrganizationId{OrganizationId: orgID}}
}

func subjectJobScope(subjectID string) *jobsv1.JobScope {
	return &jobsv1.JobScope{Value: &jobsv1.JobScope_SubjectId{SubjectId: subjectID}}
}
