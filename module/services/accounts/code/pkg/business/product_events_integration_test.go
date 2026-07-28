package business_test

import (
	"sync"
	"testing"

	"accounts/pkg/analytics"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/stretchr/testify/require"
)

func TestInvitationOutcomesExportCanonicalEvents(t *testing.T) {
	clearData(t)
	ownerID, orgID := mustUserAndOrg(
		t,
		testCtx,
		"analytics-owner@test.invalid",
		"analytics-owner",
		"Analytics Org",
	)
	invitee, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "analytics-invitee@test.invalid",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "analytics-invitee",
			ProviderEmail: "analytics-invitee@test.invalid",
		},
	})
	require.NoError(t, err)

	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(testStore))
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	outbox, err := analytics.NewOutbox(testStore, registry)
	require.NoError(t, err)
	service.SetProductAnalytics(registry, outbox)

	accepted, err := service.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID,
		Email: invitee.User.GetPrimaryEmail(),
		Role:  "member",
	})
	require.NoError(t, err)
	_, err = service.AcceptInvitation(
		testCtx,
		invitee.User.GetUuid(),
		&gen.AcceptInvitationRequest{Token: accepted.GetInviteToken()},
	)
	require.NoError(t, err)
	revoked, err := service.CreateInvitation(testCtx, ownerID, &gen.CreateInvitationRequest{
		OrgId: orgID,
		Email: "revoked-invitee@test.invalid",
		Role:  "admin",
	})
	require.NoError(t, err)
	require.NoError(t, service.RevokeInvitation(
		testCtx,
		ownerID,
		&gen.RevokeInvitationRequest{Id: revoked.GetInvitation().GetId()},
	))
	require.NoError(t, service.RevokeInvitation(
		testCtx,
		ownerID,
		&gen.RevokeInvitationRequest{Id: revoked.GetInvitation().GetId()},
	))

	workerPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(workerPool.Close)
	sink := analytics.NewMemorySink()
	jobStore := infra.NewPostgresJobStore(workerPool)
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  jobStore,
	})
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:   jobStore,
		Queue:   analytics.ExportQueue,
		Handler: handler,
	})
	require.NoError(t, err)
	processed, err := worker.RunOnce(testCtx)
	require.NoError(t, err)
	require.Equal(t, 4, processed)

	events := map[string]string{}
	for _, event := range sink.Events() {
		events[event.GetEventId()] = event.GetEventName()
		require.Equal(t, orgID, event.GetOrganizationId())
	}
	acceptedID := accepted.GetInvitation().GetId()
	revokedID := revoked.GetInvitation().GetId()
	require.Equal(
		t,
		"invite_created",
		events[analytics.DeterministicEventID("invite_created", acceptedID)],
	)
	require.Equal(
		t,
		"invite_accepted",
		events[analytics.DeterministicEventID("invite_accepted", acceptedID)],
	)
	require.Equal(
		t,
		"invite_created",
		events[analytics.DeterministicEventID("invite_created", revokedID)],
	)
	require.Equal(
		t,
		"invite_revoked",
		events[analytics.DeterministicEventID("invite_revoked", revokedID)],
	)
}

func TestDeleteUserExportsDurableAnalyticsSuppression(t *testing.T) {
	clearData(t)
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "analytics-delete@test.invalid",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "analytics-delete",
			ProviderEmail: "analytics-delete@test.invalid",
		},
	})
	require.NoError(t, err)
	userID := registered.GetUser().GetUuid()
	service, _ := analyticsService(t)

	require.NoError(t, service.DeleteUser(
		testCtx,
		userID,
		business.Identity{UserID: userID},
		&gen.GetUserRequest{Identifier: &gen.GetUserRequest_Uuid{Uuid: userID}},
	))

	sink, processed := drainAnalytics(t)
	require.Equal(t, 1, processed)
	require.Equal(t, []analytics.Suppression{{
		CommandID: analytics.DeterministicEventID("identity_suppressed", "user", userID),
		UserID:    userID,
	}}, sink.Suppressions())
}

func TestAutoDetectedOnboardingStepExportsCanonicalEvent(t *testing.T) {
	clearData(t)
	userID, _ := mustUserAndOrg(
		t,
		testCtx,
		"analytics-onboarding-auto@test.invalid",
		"analytics-onboarding-auto",
		"Analytics Auto Org",
	)
	service, _ := analyticsService(t)

	progress, err := service.GetProgress(testCtx, userID)
	require.NoError(t, err)
	require.True(t, progressStepCompleted(progress, "create_org"))

	sink, _ := drainAnalytics(t)
	var found bool
	for _, event := range sink.Events() {
		if event.GetEventName() == "onboarding_step_completed" &&
			event.GetProperties().GetFields()["step_name"].GetStringValue() == "create_org" {
			found = true
		}
	}
	require.True(t, found)
}

func TestConcurrentFinalOnboardingStepsExportCompletionExactlyOnce(t *testing.T) {
	clearData(t)
	registered, err := testService.RegisterUser(testCtx, &gen.RegisterUserRequest{
		PrimaryEmail: "analytics-onboarding-race@test.invalid",
		Identity: &gen.UserIdentity{
			Provider:      "email",
			ProviderId:    "analytics-onboarding-race",
			ProviderEmail: "analytics-onboarding-race@test.invalid",
		},
	})
	require.NoError(t, err)
	userID := registered.GetUser().GetUuid()
	service, _ := analyticsService(t)
	require.NoError(t, service.CompleteStep(testCtx, userID, "create_org"))
	require.NoError(t, service.CompleteStep(testCtx, userID, "invite_team"))

	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for _, step := range []string{"choose_plan", "setup_api_key"} {
		step := step
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- service.CompleteStep(testCtx, userID, step)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		require.NoError(t, err)
	}

	sink, processed := drainAnalytics(t)
	require.Equal(t, 5, processed)
	var completions int
	for _, event := range sink.Events() {
		if event.GetEventName() == "onboarding_completed" {
			completions++
		}
	}
	require.Equal(t, 1, completions)
}

func analyticsService(t *testing.T) (*business.Service, *analytics.Registry) {
	t.Helper()
	service, err := business.NewService(testStore)
	require.NoError(t, err)
	service.SetEntitlementChecker(business.NewDefaultEntitlementChecker(testStore))
	registry, err := analytics.DefaultRegistry()
	require.NoError(t, err)
	outbox, err := analytics.NewOutbox(testStore, registry)
	require.NoError(t, err)
	service.SetProductAnalytics(registry, outbox)
	return service, registry
}

func drainAnalytics(t *testing.T) (*analytics.MemorySink, int) {
	t.Helper()
	workerPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(workerPool.Close)
	jobStore := infra.NewPostgresJobStore(workerPool)
	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(analytics.ExportHandlerConfig{
		Destination: sink,
		Deliveries:  jobStore,
	})
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: jobStore, Queue: analytics.ExportQueue, Handler: handler,
	})
	require.NoError(t, err)
	processed, err := worker.RunOnce(testCtx)
	require.NoError(t, err)
	return sink, processed
}

func progressStepCompleted(progress *business.OnboardingProgress, name string) bool {
	for _, step := range progress.Steps {
		if step.StepName == name {
			return step.Status == "completed"
		}
	}
	return false
}
