package business_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

type invitationLifecycleStore struct {
	business.Store
	invitation             *business.Invitation
	user                   *gen.User
	concurrentStatusChange string
}

func (f *invitationLifecycleStore) As(identity business.Identity) business.Scoped {
	return waitlistScopedFake{identity: identity}
}

func (*invitationLifecycleStore) WithOrgTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (*invitationLifecycleStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (f *invitationLifecycleStore) GetInvitationByID(context.Context, string) (*business.Invitation, error) {
	if f.invitation == nil {
		return nil, nil
	}
	copy := *f.invitation
	return &copy, nil
}

func (f *invitationLifecycleStore) GetInvitationByTokenHash(
	_ context.Context,
	hash string,
) (*business.Invitation, error) {
	if f.invitation == nil || f.invitation.TokenHash != hash {
		return nil, nil
	}
	copy := *f.invitation
	return &copy, nil
}

func (f *invitationLifecycleStore) GetUser(context.Context, string) (*gen.User, error) {
	return f.user, nil
}

func (f *invitationLifecycleStore) RotateInvitationToken(_ context.Context, invitation *business.Invitation) error {
	copy := *invitation
	f.invitation = &copy
	return nil
}

func (*invitationLifecycleStore) GetOrganization(context.Context, string) (*gen.Organization, error) {
	return &gen.Organization{Name: "Acme"}, nil
}

func (f *invitationLifecycleStore) UpdateInvitationStatus(
	_ context.Context,
	_ string,
	status string,
	acceptedBy string,
) (bool, error) {
	if f.invitation == nil || f.invitation.Status != "pending" {
		return false, nil
	}
	if f.concurrentStatusChange != "" {
		f.invitation.Status = f.concurrentStatusChange
		f.concurrentStatusChange = ""
		return false, nil
	}
	f.invitation.Status = status
	f.invitation.AcceptedBy = acceptedBy
	return true, nil
}

type invitationJobProducer struct {
	enqueued int
	requests []*jobsv1.EnqueueJobRequest
}

func (p *invitationJobProducer) EnqueueJob(
	_ context.Context,
	request *jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	p.enqueued++
	p.requests = append(p.requests, request)
	return &jobsv1.EnqueueJobResponse{
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func newInvitationLifecycleService(
	t *testing.T,
	store *invitationLifecycleStore,
	withDelivery bool,
) (*business.Service, *invitationJobProducer) {
	t.Helper()
	service, err := business.NewService(store)
	require.NoError(t, err)
	producer := &invitationJobProducer{}
	if withDelivery {
		outbox, err := email.NewOutbox(producer, nil, "no-reply@example.com")
		require.NoError(t, err)
		service.SetEmailOutbox(outbox, "https://app.example.com")
	}
	return service, producer
}

func pendingLifecycleInvitation() *business.Invitation {
	return &business.Invitation{
		ID:                 "00000000-0000-4000-8000-000000000030",
		OrgID:              "00000000-0000-4000-8000-000000000031",
		InviterID:          "00000000-0000-4000-8000-000000000032",
		InviterDisplayName: "Admin",
		Email:              "invitee@example.com",
		Role:               "member",
		TokenHash:          "old-token-hash",
		Status:             "pending",
		DeliveryStatus:     "queued",
		ExpiresAt:          time.Now().Add(24 * time.Hour),
	}
}

func TestResendInvitationResolvesIdempotentRetryBeforeCooldown(t *testing.T) {
	store := &invitationLifecycleStore{invitation: pendingLifecycleInvitation()}
	service, producer := newInvitationLifecycleService(t, store, true)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)
	request := &gen.ResendInvitationRequest{Id: store.invitation.ID}

	first, err := service.ResendInvitation(context.Background(), "admin", request, "same-operation")
	require.NoError(t, err)
	replay, err := service.ResendInvitation(context.Background(), "admin", request, "same-operation")
	require.NoError(t, err)

	require.Equal(t, first.SendCount, replay.SendCount)
	require.Equal(t, uint32(1), replay.SendCount)
	require.Equal(t, 1, producer.enqueued)
	require.Len(t, audit.entries, 1)

	_, err = service.ResendInvitation(context.Background(), "admin", request, "different-operation")
	require.ErrorContains(t, err, "sent recently")
	require.Equal(t, 1, producer.enqueued)
}

func TestResendInvitationDoesNotRotateWhenDeliveryIsDisabled(t *testing.T) {
	store := &invitationLifecycleStore{invitation: pendingLifecycleInvitation()}
	service, _ := newInvitationLifecycleService(t, store, false)
	originalHash := store.invitation.TokenHash

	_, err := service.ResendInvitation(
		context.Background(),
		"admin",
		&gen.ResendInvitationRequest{Id: store.invitation.ID},
		"resend-operation",
	)

	require.ErrorContains(t, err, "delivery is unavailable")
	require.Equal(t, originalHash, store.invitation.TokenHash)
	require.Zero(t, store.invitation.SendCount)
}

func TestRevokeInvitationOnlyEmitsForPendingTransition(t *testing.T) {
	store := &invitationLifecycleStore{invitation: pendingLifecycleInvitation()}
	service, _ := newInvitationLifecycleService(t, store, false)
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)
	request := &gen.RevokeInvitationRequest{Id: store.invitation.ID}

	require.NoError(t, service.RevokeInvitation(context.Background(), "admin", request))
	require.NoError(t, service.RevokeInvitation(context.Background(), "admin", request))
	require.Equal(t, "revoked", store.invitation.Status)
	require.Len(t, audit.entries, 1)

	store.invitation.Status = "accepted"
	err := service.RevokeInvitation(context.Background(), "admin", request)
	require.ErrorIs(t, err, business.ErrInvitationUnavailable)
	require.Len(t, audit.entries, 1)
}

func TestInspectInvitationByIDReturnsTheAuthoritativeStatusToTheInvitee(t *testing.T) {
	store := &invitationLifecycleStore{
		invitation: pendingLifecycleInvitation(),
		user:       &gen.User{PrimaryEmail: "invitee@example.com"},
	}
	store.invitation.Status = "revoked"
	service, _ := newInvitationLifecycleService(t, store, false)

	summary, err := service.InspectInvitationByID(
		context.Background(),
		"invitee",
		&gen.InspectInvitationByIdRequest{
			InvitationId: store.invitation.ID,
		},
	)

	require.NoError(t, err)
	require.Equal(t, gen.InvitationStatus_INVITATION_STATUS_REVOKED, summary.Status)

	store.user.PrimaryEmail = "other@example.com"
	_, err = service.InspectInvitationByID(
		context.Background(),
		"other-user",
		&gen.InspectInvitationByIdRequest{
			InvitationId: store.invitation.ID,
		},
	)
	require.ErrorIs(t, err, business.ErrInvitationEmailMismatch)
}

// The emailed token reaches the browser in a query the proxy moves into a
// fifteen-minute cookie, so the inspect before sign-in is the only chance to
// learn WHICH invitation the link names. The summary therefore carries the id,
// and the browser returns by it rather than by a cookie that has to outlive a
// sign-up, an MFA enrolment or a passkey ceremony. The id is no credential:
// accepting by it still requires a signed-in account whose primary email
// matches the invitation's, which
// TestInspectInvitationByIDReturnsTheAuthoritativeStatusToTheInvitee holds.
func TestInspectInvitationReportsTheInvitationIDToAnUnauthenticatedCaller(t *testing.T) {
	const token = "a-plaintext-invitation-token-of-ample-length"
	store := &invitationLifecycleStore{invitation: pendingLifecycleInvitation()}
	store.invitation.TokenHash = business.HashInvitationToken(token)
	service, _ := newInvitationLifecycleService(t, store, false)

	summary, err := service.InspectInvitation(
		context.Background(),
		&gen.InspectInvitationRequest{Token: token},
	)

	require.NoError(t, err)
	require.Equal(t, store.invitation.ID, summary.Id)
	require.Equal(t, gen.InvitationStatus_INVITATION_STATUS_PENDING, summary.Status)
	require.NotEqual(t, store.invitation.Email, summary.EmailHint)
}

func TestInspectInvitationDoesNotOverwriteAConcurrentTerminalStatus(t *testing.T) {
	store := &invitationLifecycleStore{
		invitation:             pendingLifecycleInvitation(),
		user:                   &gen.User{PrimaryEmail: "invitee@example.com"},
		concurrentStatusChange: "accepted",
	}
	store.invitation.ExpiresAt = time.Now().Add(-time.Minute)
	service, _ := newInvitationLifecycleService(t, store, false)

	summary, err := service.InspectInvitationByID(
		context.Background(),
		"invitee",
		&gen.InspectInvitationByIdRequest{
			InvitationId: store.invitation.ID,
		},
	)

	require.NoError(t, err)
	require.Equal(t, gen.InvitationStatus_INVITATION_STATUS_ACCEPTED, summary.Status)
}
