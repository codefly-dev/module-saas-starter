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
	count uint64
	err   error
	reads int
}

func (c *countedInbox) CountDeliveredDocuments(context.Context) (uint64, error) {
	c.reads++
	return c.count, c.err
}

// A principal the registry does not hold hears a judgement — unless the inbox
// holds a document the registry was derived without. A workload that starts
// beside its own delivery asks in exactly that moment, and a refusal there ends
// it for a state the host is one pass away from leaving.
func TestAnUndeclaredPrincipalIsNotRefusedWhileADeliveryIsUnread(t *testing.T) {
	const unknown = "principal-nobody-declared"
	marker := func(n uint64) *uint64 { return &n }
	setup := func(delivered *uint64, inbox *countedInbox) (*Service, *int) {
		s := reviewService()
		s.deliveryStore = inbox
		passes := 0
		s.SetModuleRegistryPassTrigger(func() { passes++ })
		s.installDerivedModulePrincipals(ModulePrincipalRegistry{"principal-1": {Prefix: "crm", Tenant: "org-1"}}, 0, delivered)
		return s, &passes
	}

	t.Run("nothing delivered since: the judgement stands", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{count: 2})
		_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), unknown)
		require.NoError(t, err)
		require.False(t, found)
		require.Zero(t, *passes)
	})

	t.Run("a document delivered since: not established yet, and a pass is asked for", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{count: 3})
		_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), unknown)
		require.False(t, found)
		require.ErrorIs(t, err, ErrModuleRegistryStale)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Equal(t, 1, *passes)
	})

	t.Run("the pass read it and still grants this principal nothing: the judgement again", func(t *testing.T) {
		inbox := &countedInbox{count: 3}
		s, _ := setup(marker(2), inbox)
		s.installDerivedModulePrincipals(ModulePrincipalRegistry{}, 0, marker(3))
		_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), unknown)
		require.NoError(t, err)
		require.False(t, found)
	})

	t.Run("a registry with no marker answers as it always did", func(t *testing.T) {
		inbox := &countedInbox{count: 99}
		s := reviewService()
		s.deliveryStore = inbox
		s.InstallDerivedModulePrincipals(ModulePrincipalRegistry{}, 0)
		_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), unknown)
		require.NoError(t, err)
		require.False(t, found)
		require.Zero(t, inbox.reads, "an unmarked registry is not compared with the inbox")
	})

	t.Run("an inbox that cannot be counted changes nothing", func(t *testing.T) {
		s, passes := setup(marker(2), &countedInbox{err: errors.New("unreadable")})
		_, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), unknown)
		require.NoError(t, err)
		require.False(t, found)
		require.Zero(t, *passes)
	})

	t.Run("a declared principal is never held back by it", func(t *testing.T) {
		inbox := &countedInbox{count: 3}
		s, _ := setup(marker(2), inbox)
		grant, found, err := s.currentModuleGrantFromSnapshot(context.Background(), s.moduleRegistrySnapshot(), "principal-1")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "crm", grant.Prefix)
		require.Zero(t, inbox.reads)
	})
}
