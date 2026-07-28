package business_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
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
) (*business.WaitlistEntry, bool, error) {
	if existing := f.entries[entry.Email]; existing != nil {
		return existing, false, nil
	}
	copy := *entry
	f.entries[entry.Email] = &copy
	return &copy, true, nil
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
