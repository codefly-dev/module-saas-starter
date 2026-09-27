package membership_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cache "github.com/codefly-dev/interface-cache/go/cache"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/membership"
)

// These tests put cache.Memory where a running service has Redis. The same
// behaviour against a real Redis and the real store is the redis-db suite
// (pkg/redisintegration).

type countingStore struct {
	mu    sync.Mutex
	roles map[string]gen.OrgRole
	err   error
	loads atomic.Int64
}

func newCountingStore() *countingStore {
	return &countingStore{roles: map[string]gen.OrgRole{}}
}

func (s *countingStore) set(orgID, userID string, role gen.OrgRole) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role == gen.OrgRole_ORG_ROLE_UNSPECIFIED {
		delete(s.roles, orgID+"/"+userID)
		return
	}
	s.roles[orgID+"/"+userID] = role
}

func (s *countingStore) GetOrgMembership(_ context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	s.loads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	role, ok := s.roles[orgID+"/"+userID]
	if !ok {
		return nil, nil
	}
	return &gen.OrgMembership{OrgId: orgID, UserId: userID, Role: role}, nil
}

func verified(orgID, userID string) context.Context {
	return auth.WithVerifiedDatabaseIdentity(context.Background(), userID, orgID)
}

func newCache(t *testing.T, shared cache.Layer, store membership.Store) *membership.Cache {
	t.Helper()
	c, err := membership.New(context.Background(), shared, store)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func TestRoleIsLoadedOnceThenServedFromTheStack(t *testing.T) {
	store := newCountingStore()
	org, user := uuid.NewString(), uuid.NewString()
	store.set(org, user, gen.OrgRole_ORG_ROLE_ADMIN)
	c := newCache(t, cache.NewMemory(), store)

	for range 3 {
		role, err := c.Role(verified(org, user), org, user)
		require.NoError(t, err)
		require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN.String(), role)
	}
	require.EqualValues(t, 1, store.loads.Load())
}

func TestNonMemberIsCachedAndInvalidatedLikeAMember(t *testing.T) {
	store := newCountingStore()
	org, user := uuid.NewString(), uuid.NewString()
	c := newCache(t, cache.NewMemory(), store)
	ctx := verified(org, user)

	role, err := c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Empty(t, role, "no row is a verified non-member")
	role, err = c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Empty(t, role)
	require.EqualValues(t, 1, store.loads.Load(), "the negative answer is cached")

	store.set(org, user, gen.OrgRole_ORG_ROLE_MEMBER)
	require.NoError(t, c.Invalidate(context.Background(), org, user))
	role, err = c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
}

func TestRoleRefusesACallerWhoseVerifiedIdentityIsNotTheKey(t *testing.T) {
	store := newCountingStore()
	org, user, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store.set(org, user, gen.OrgRole_ORG_ROLE_OWNER)
	c := newCache(t, cache.NewMemory(), store)

	// Warm the entry as its own member, then ask for it as someone else.
	_, err := c.Role(verified(org, user), org, user)
	require.NoError(t, err)

	_, err = c.Role(verified(org, other), org, user)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch, "a warm entry is not served to another user")
	_, err = c.Role(context.Background(), org, user)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseIdentityRequired)
	require.EqualValues(t, 1, store.loads.Load())
}

func TestStoreErrorIsReturnedAndNotCached(t *testing.T) {
	store := newCountingStore()
	org, user := uuid.NewString(), uuid.NewString()
	store.set(org, user, gen.OrgRole_ORG_ROLE_MEMBER)
	boom := errors.New("store unreachable")
	store.err = boom
	c := newCache(t, cache.NewMemory(), store)
	ctx := verified(org, user)

	_, err := c.Role(ctx, org, user)
	require.ErrorIs(t, err, boom)

	store.mu.Lock()
	store.err = nil
	store.mu.Unlock()
	role, err := c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
}

// Two instances over one shared layer: the second reads the first's fill, and
// an invalidation on either evicts the other's in-process copy.
func TestTwoInstancesShareFillsAndInvalidations(t *testing.T) {
	store := newCountingStore()
	org, user := uuid.NewString(), uuid.NewString()
	store.set(org, user, gen.OrgRole_ORG_ROLE_MEMBER)
	shared := cache.NewMemory()
	a := newCache(t, shared, store)
	b := newCache(t, shared.Share(), store)
	ctx := verified(org, user)

	for _, c := range []*membership.Cache{a, b, a, b} {
		role, err := c.Role(ctx, org, user)
		require.NoError(t, err)
		require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
	}
	require.EqualValues(t, 1, store.loads.Load())

	store.set(org, user, gen.OrgRole_ORG_ROLE_ADMIN)
	require.NoError(t, a.Invalidate(context.Background(), org, user))
	require.Eventually(t, func() bool {
		role, err := b.Role(ctx, org, user)
		return err == nil && role == gen.OrgRole_ORG_ROLE_ADMIN.String()
	}, 2*time.Second, 10*time.Millisecond, "b's in-process copy must be evicted by a's invalidation")
}

func TestInvalidateOrgReachesEveryListedMember(t *testing.T) {
	store := newCountingStore()
	org := uuid.NewString()
	users := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	c := newCache(t, cache.NewMemory(), store)
	for _, user := range users {
		store.set(org, user, gen.OrgRole_ORG_ROLE_MEMBER)
		_, err := c.Role(verified(org, user), org, user)
		require.NoError(t, err)
	}

	for _, user := range users {
		store.set(org, user, gen.OrgRole_ORG_ROLE_UNSPECIFIED)
	}
	require.NoError(t, c.InvalidateOrg(context.Background(), org, users))
	for _, user := range users {
		role, err := c.Role(verified(org, user), org, user)
		require.NoError(t, err)
		require.Empty(t, role)
	}
}

func TestInvalidateRefusesAnEmptyKey(t *testing.T) {
	c := newCache(t, cache.NewMemory(), newCountingStore())
	require.Error(t, c.Invalidate(context.Background(), "", uuid.NewString()))
	require.Error(t, c.Invalidate(context.Background(), uuid.NewString(), ""))
}
