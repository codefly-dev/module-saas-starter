package business_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/abuse"
	"accounts/pkg/business"
	"accounts/pkg/email"
	gen "accounts/pkg/gen/saas/accounts/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
)

type waitlistStoreFake struct {
	business.Store
	entries map[string]*business.WaitlistEntry
}

type waitlistScopedFake struct {
	identity business.Identity
}

func (s waitlistScopedFake) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s waitlistScopedFake) Identity() business.Identity {
	return s.identity
}

func (s waitlistScopedFake) ListPrincipals(context.Context, string, int32, string) ([]*business.Principal, string, error) {
	panic("unexpected ListPrincipals call")
}

func (s waitlistScopedFake) AddOrgMember(context.Context, string, string) error {
	panic("unexpected AddOrgMember call")
}

func (s waitlistScopedFake) GetPrincipal(context.Context, string) (*business.Principal, error) {
	panic("unexpected GetPrincipal call")
}

func (s waitlistScopedFake) GetAgentPrincipal(context.Context, string, string) (*business.Principal, error) {
	panic("unexpected GetAgentPrincipal call")
}

func (s waitlistScopedFake) CreateAgentPrincipal(context.Context, *business.Principal) error {
	panic("unexpected CreateAgentPrincipal call")
}

func (s waitlistScopedFake) RevokePrincipal(context.Context, string, string) error {
	panic("unexpected RevokePrincipal call")
}

func newWaitlistStoreFake() *waitlistStoreFake {
	return &waitlistStoreFake{entries: make(map[string]*business.WaitlistEntry)}
}

func (f *waitlistStoreFake) As(identity business.Identity) business.Scoped {
	return waitlistScopedFake{identity: identity}
}

func (f *waitlistStoreFake) GetWaitlistReferralID(context.Context, string) (string, error) {
	return "", nil
}

func (f *waitlistStoreFake) UpsertWaitlistEntry(
	_ context.Context,
	entry *business.WaitlistEntry,
	_ time.Duration,
) (*business.WaitlistUpsertResult, error) {
	if existing := f.entries[entry.Email]; existing != nil {
		return &business.WaitlistUpsertResult{Entry: existing}, nil
	}
	copy := *entry
	f.entries[entry.Email] = &copy
	return &business.WaitlistUpsertResult{
		Entry:                  &copy,
		Created:                true,
		ShouldSendVerification: copy.State == "pending",
	}, nil
}

func (f *waitlistStoreFake) VerifyWaitlistEntry(
	_ context.Context,
	tokenHash string,
	now time.Time,
) (*business.WaitlistVerificationResult, error) {
	for _, entry := range f.entries {
		if entry.VerificationTokenHash != tokenHash {
			continue
		}
		transitioned := entry.State == "pending"
		if transitioned {
			entry.State = "verified"
			entry.VerifiedAt = &now
		}
		return &business.WaitlistVerificationResult{
			Entry:        entry,
			Transitioned: transitioned,
		}, nil
	}
	return &business.WaitlistVerificationResult{}, nil
}

func (f *waitlistStoreFake) InviteWaitlistEntry(
	_ context.Context,
	id string,
	now time.Time,
) (*business.WaitlistEntry, error) {
	for _, entry := range f.entries {
		if entry.ID != id || entry.State != "approved" {
			continue
		}
		entry.State = "invited"
		entry.InvitedAt = &now
		return entry, nil
	}
	return nil, nil
}

type waitlistJobProducer struct {
	enqueued int
}

type rejectingAbuseVerifier struct {
	challenges []abuse.Challenge
}

func (v *rejectingAbuseVerifier) Verify(_ context.Context, challenge abuse.Challenge) error {
	v.challenges = append(v.challenges, challenge)
	return abuse.ErrChallengeRejected
}

func TestAbuseChallengeFailurePreventsRegistrationAndWaitlistWrites(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.SetAcquisitionMode("approval_required"))
	verifier := &rejectingAbuseVerifier{}
	service.SetAbuseVerifier(verifier)

	_, err = service.JoinWaitlist(context.Background(), &gen.JoinWaitlistRequest{
		Email:          "person@example.com",
		PolicyVersion:  business.CurrentConsentPolicyVersion,
		TurnstileToken: "waitlist-token",
	})
	require.ErrorIs(t, err, abuse.ErrChallengeRejected)
	require.Empty(t, store.entries)
	require.Equal(t, abuse.Challenge{
		Token: "waitlist-token", Action: "waitlist_join",
	}, verifier.challenges[0])

	_, err = service.RegisterUser(context.Background(), &gen.RegisterUserRequest{
		PrimaryEmail:   "person@example.com",
		TurnstileToken: "registration-token",
	})
	require.ErrorIs(t, err, abuse.ErrChallengeRejected)
	require.Empty(t, store.entries)
	require.Equal(t, abuse.Challenge{
		Token: "registration-token", Action: "register_user",
	}, verifier.challenges[1])
}

func (p *waitlistJobProducer) EnqueueJob(
	context.Context,
	*jobsv1.EnqueueJobRequest,
) (*jobsv1.EnqueueJobResponse, error) {
	p.enqueued++
	return &jobsv1.EnqueueJobResponse{
		Disposition: jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED,
	}, nil
}

func TestJoinWaitlistIsCaseFoldedAndEnumerationSafe(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.SetAcquisitionMode("approval_required"))

	first, err := service.JoinWaitlist(context.Background(), &gen.JoinWaitlistRequest{
		Email:         "  Person@Example.COM ",
		PolicyVersion: business.CurrentConsentPolicyVersion,
	})
	require.NoError(t, err)
	second, err := service.JoinWaitlist(context.Background(), &gen.JoinWaitlistRequest{
		Email:         "person@example.com",
		PolicyVersion: business.CurrentConsentPolicyVersion,
	})
	require.NoError(t, err)

	require.Equal(t, first.Message, second.Message)
	require.Len(t, store.entries, 1)
	entry := store.entries["person@example.com"]
	require.NotNil(t, entry)
	require.Equal(t, "pending", entry.State)
	require.Len(t, entry.VerificationTokenHash, 64)
	require.False(t, strings.Contains(first.Message, entry.Email))
}

func TestClosedAcquisitionModeDoesNotPersistWaitlistData(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.SetAcquisitionMode("closed"))

	response, err := service.JoinWaitlist(context.Background(), &gen.JoinWaitlistRequest{
		Email:         "person@example.com",
		PolicyVersion: business.CurrentConsentPolicyVersion,
	})

	require.NoError(t, err)
	require.NotEmpty(t, response.Message)
	require.Empty(t, store.entries)
}

func TestJoinWaitlistRejectsStaleConsentPolicy(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.SetAcquisitionMode("approval_required"))

	_, err = service.JoinWaitlist(context.Background(), &gen.JoinWaitlistRequest{
		Email:         "person@example.com",
		PolicyVersion: "attacker-controlled-version",
	})

	require.ErrorContains(t, err, "policy version")
	require.Empty(t, store.entries)
}

func TestWaitlistJourneyEventsEmitOnlyOnStateTransition(t *testing.T) {
	store := newWaitlistStoreFake()
	service, err := business.NewService(store)
	require.NoError(t, err)
	require.NoError(t, service.SetAcquisitionMode("approval_required"))
	audit := &recordingAuditEmitter{}
	service.SetAuditEmitter(audit)

	request := &gen.JoinWaitlistRequest{
		Email:         "person@example.com",
		PolicyVersion: business.CurrentConsentPolicyVersion,
	}
	_, err = service.JoinWaitlist(context.Background(), request)
	require.NoError(t, err)
	_, err = service.JoinWaitlist(context.Background(), request)
	require.NoError(t, err)

	token := "verification-token"
	hash := sha256.Sum256([]byte(token))
	entry := store.entries["person@example.com"]
	entry.VerificationTokenHash = hex.EncodeToString(hash[:])
	_, err = service.VerifyWaitlist(context.Background(), &gen.VerifyWaitlistRequest{Token: token})
	require.NoError(t, err)
	_, err = service.VerifyWaitlist(context.Background(), &gen.VerifyWaitlistRequest{Token: token})
	require.NoError(t, err)

	require.Len(t, audit.entries, 2)
	require.Equal(t, "waitlist.joined", audit.entries[0].Action)
	require.Equal(t, "waitlist.verified", audit.entries[1].Action)
}

func TestInviteWaitlistRequiresDeliveryAndApproval(t *testing.T) {
	store := newWaitlistStoreFake()
	store.entries["person@example.com"] = &business.WaitlistEntry{
		ID:    "00000000-0000-4000-8000-000000000020",
		Email: "person@example.com",
		State: "approved",
	}
	service, err := business.NewService(store)
	require.NoError(t, err)

	_, err = service.InviteWaitlist(context.Background(), "admin", &gen.InviteWaitlistRequest{
		Id: "00000000-0000-4000-8000-000000000020",
	})
	require.ErrorContains(t, err, "delivery is unavailable")
	require.Equal(t, "approved", store.entries["person@example.com"].State)

	producer := &waitlistJobProducer{}
	outbox, err := email.NewOutbox(producer, nil, "no-reply@example.com")
	require.NoError(t, err)
	service.SetEmailOutbox(outbox, "https://app.example.com")

	store.entries["person@example.com"].State = "pending"
	_, err = service.InviteWaitlist(context.Background(), "admin", &gen.InviteWaitlistRequest{
		Id: "00000000-0000-4000-8000-000000000020",
	})
	require.ErrorContains(t, err, "not found")
	require.Equal(t, "pending", store.entries["person@example.com"].State)
	require.Zero(t, producer.enqueued)

	store.entries["person@example.com"].State = "approved"
	_, err = service.InviteWaitlist(context.Background(), "admin", &gen.InviteWaitlistRequest{
		Id: "00000000-0000-4000-8000-000000000020",
	})
	require.NoError(t, err)
	require.Equal(t, "invited", store.entries["person@example.com"].State)
	require.Equal(t, 1, producer.enqueued)
}
