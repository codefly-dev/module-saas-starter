package business_test

import (
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

	workerPool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(workerPool.Close)
	sink := analytics.NewMemorySink()
	handler, err := analytics.NewExportHandler(registry, sink)
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store:   infra.NewPostgresJobStore(workerPool),
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
