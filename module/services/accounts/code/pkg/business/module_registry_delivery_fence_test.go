package business

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// countedInbox is a delivery inbox that can say how many documents it holds.
type countedInbox struct {
	SolutionDeliveryStore
	count     uint64
	unapplied uint64
	err       error
	reads     int
}

func (c *countedInbox) CountDeliveredDocuments(context.Context) (uint64, error) {
	c.reads++
	return c.count, c.err
}

func (c *countedInbox) CountUnappliedManifests(context.Context) (uint64, error) {
	return c.unapplied, c.err
}

// A prefix the registry does not hold is refused at its mint — unless the
// inbox holds a document the registry was derived without. A workload that
// starts beside its own delivery asks in exactly that moment, and a refusal
// there ends it for a state the host is one pass away from leaving.
func TestAnUndeclaredModuleIsNotRefusedWhileADeliveryIsUnread(t *testing.T) {
	const prefix = "nobody-declared"
	marker := func(n uint64) *uint64 { return &n }
	setup := func(delivered *uint64, inbox *countedInbox) (*Service, *int) {
		s := reviewService()
		s.deliveryStore = inbox
		passes := 0
		s.SetModuleRegistryPassTrigger(func() { passes++ })
		s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1"}}, 0, delivered)
		return s, &passes
	}
	mint := func(s *Service) error {
		_, _, err := s.authorizeModuleIdentity(context.Background(), s.moduleRegistrySnapshot(), prefix, "projected-token")
		return err
	}

	t.Run("nothing delivered since: the refusal stands", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{count: 2})
		require.ErrorIs(t, mint(s), ErrModuleIdentityDenied)
		require.Zero(t, *passes)
	})
	t.Run("a document delivered since: not established yet, and a pass is asked for", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{count: 3})
		err := mint(s)
		require.ErrorIs(t, err, ErrModuleRegistryStale)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Equal(t, 1, *passes)
	})
	t.Run("the pass read everything but a presence is not applied yet: not established yet", func(t *testing.T) {
		s, passes := setup(marker(3), &countedInbox{count: 3, unapplied: 1})
		err := mint(s)
		require.ErrorIs(t, err, ErrModuleRegistryStale)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Equal(t, 1, *passes)
	})
	t.Run("the pass read it and still declares nothing: the refusal again", func(t *testing.T) {
		s, _ := setup(marker(2), &countedInbox{count: 3})
		s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{}, 0, marker(3))
		require.ErrorIs(t, mint(s), ErrModuleIdentityDenied)
	})
	t.Run("a registry with no marker answers as it always did", func(t *testing.T) {
		inbox := &countedInbox{count: 99}
		s, _ := setup(nil, inbox)
		require.ErrorIs(t, mint(s), ErrModuleIdentityDenied)
		require.Zero(t, inbox.reads)
	})
	t.Run("an inbox that cannot be counted changes nothing", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{err: errors.New("unreadable")})
		require.ErrorIs(t, mint(s), ErrModuleIdentityDenied)
		require.Zero(t, *passes)
	})
}

// Every other reader of the registry — a person's revision check asks it "is
// this owner a module?" — is untouched by an unread delivery: it neither waits
// nor pays for a count.
func TestAnUnreadDeliveryChangesNoOtherRegistryAnswer(t *testing.T) {
	inbox := &countedInbox{count: 3}
	s := reviewService()
	s.deliveryStore = inbox
	n := uint64(2)
	s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1"}}, 0, &n)
	_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), "a-person")
	require.NoError(t, err)
	require.False(t, found)
	grant, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), "principal-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "crm", grant.Prefix)
	require.Zero(t, inbox.reads)
}
