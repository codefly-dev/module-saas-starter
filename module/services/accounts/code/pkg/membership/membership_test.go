package membership_test

import (
	"context"
	"errors"
	"strings"
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

func TestInvalidateRefusesAnIDThatIsNotAUUID(t *testing.T) {
	c := newCache(t, cache.NewMemory(), newCountingStore())
	require.Error(t, c.Invalidate(context.Background(), "", uuid.NewString()))
	require.Error(t, c.Invalidate(context.Background(), uuid.NewString(), ""))
	require.Error(t, c.Invalidate(context.Background(), "acme", uuid.NewString()))
	require.Error(t, c.Invalidate(context.Background(), uuid.Nil.String(), uuid.NewString()))
}

// The scope check and the store accept every spelling uuid.Parse does. One
// membership must still be one key: an invalidation under any spelling reaches
// a copy cached under any other.
func TestEverySpellingOfAnIDIsOneKey(t *testing.T) {
	spellings := map[string]func(string) string{
		"upper":  strings.ToUpper,
		"braced": func(id string) string { return "{" + id + "}" },
		"urn":    func(id string) string { return "urn:uuid:" + id },
		"bare":   func(id string) string { return strings.ReplaceAll(id, "-", "") },
	}
	for name, spell := range spellings {
		t.Run(name, func(t *testing.T) {
			store := newCountingStore()
			org, user := uuid.NewString(), uuid.NewString()
			store.set(org, user, gen.OrgRole_ORG_ROLE_ADMIN)
			c := newCache(t, cache.NewMemory(), store)
			ctx := verified(org, user)

			// Warmed under the alternate spelling, invalidated canonically.
			role, err := c.Role(ctx, spell(org), spell(user))
			require.NoError(t, err)
			require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN.String(), role)
			store.set(org, user, gen.OrgRole_ORG_ROLE_UNSPECIFIED)
			require.NoError(t, c.Invalidate(context.Background(), org, user))
			role, err = c.Role(ctx, spell(org), spell(user))
			require.NoError(t, err)
			require.Empty(t, role, "a canonical invalidation must reach the %s spelling", name)

			// Warmed canonically, invalidated under the alternate spelling.
			store.set(org, user, gen.OrgRole_ORG_ROLE_MEMBER)
			require.NoError(t, c.Invalidate(context.Background(), org, user))
			role, err = c.Role(ctx, org, user)
			require.NoError(t, err)
			require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
			store.set(org, user, gen.OrgRole_ORG_ROLE_UNSPECIFIED)
			require.NoError(t, c.Invalidate(context.Background(), spell(org), spell(user)))
			role, err = c.Role(ctx, org, user)
			require.NoError(t, err)
			require.Empty(t, role, "a %s-spelled invalidation must reach the canonical copy", name)
		})
	}
}

func TestInvalidateRunsTheEvictionHookWithCanonicalIDs(t *testing.T) {
	var got [][2]string
	c, err := membership.New(context.Background(), cache.NewMemory(), newCountingStore(),
		membership.WithEviction(func(_ context.Context, orgID, userID string) error {
			got = append(got, [2]string{orgID, userID})
			return nil
		}))
	require.NoError(t, err)
	t.Cleanup(c.Close)
	org, user := uuid.NewString(), uuid.NewString()

	require.NoError(t, c.Invalidate(context.Background(), strings.ToUpper(org), "{"+user+"}"))
	require.Equal(t, [][2]string{{org, user}}, got)

	boom := errors.New("legacy eviction failed")
	c, err = membership.New(context.Background(), cache.NewMemory(), newCountingStore(),
		membership.WithEviction(func(context.Context, string, string) error { return boom }))
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.ErrorIs(t, c.Invalidate(context.Background(), org, user), boom, "an eviction failure is reported")
}

// unreachable is a shared layer whose change notices cannot be subscribed to
// until it has refused `failures` times: the Notifier failure Connect retries.
// The real unreachable server is exercised by the redis-db suite.
type unreachable struct {
	*cache.Memory
	failures int
	calls    atomic.Int64
}

func (u *unreachable) Subscribe(ctx context.Context, fn func(string)) (func(), error) {
	if u.calls.Add(1) <= int64(u.failures) {
		return nil, errors.New("dial tcp: connection refused")
	}
	return u.Memory.Subscribe(ctx, fn)
}

var fastRetry = membership.Retry{Attempts: 3, Wait: time.Millisecond, AttemptTimeout: time.Second}

func TestConnectRetriesAnUnreachableLayerWithinItsBudget(t *testing.T) {
	store := newCountingStore()
	shared := &unreachable{Memory: cache.NewMemory(), failures: 2}
	c, err := membership.Connect(context.Background(), shared, store, fastRetry)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.StoreOnly(), "the third attempt reaches the layer")
	require.EqualValues(t, 3, shared.calls.Load())
}

func TestConnectFallsBackToStoreOnlyWhichIsNeverStale(t *testing.T) {
	store := newCountingStore()
	org, user := uuid.NewString(), uuid.NewString()
	store.set(org, user, gen.OrgRole_ORG_ROLE_ADMIN)
	shared := &unreachable{Memory: cache.NewMemory(), failures: 1 << 30}
	var evicted atomic.Int64
	c, err := membership.Connect(context.Background(), shared, store, fastRetry,
		membership.WithEviction(func(context.Context, string, string) error { evicted.Add(1); return nil }))
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.ErrorContains(t, c.StoreOnly(), "attempt 3 of 3")
	require.EqualValues(t, 3, shared.calls.Load())
	ctx := verified(org, user)

	role, err := c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN.String(), role)
	// No invalidation: store-only reads the removal on the very next call.
	store.set(org, user, gen.OrgRole_ORG_ROLE_UNSPECIFIED)
	role, err = c.Role(ctx, org, user)
	require.NoError(t, err)
	require.Empty(t, role)
	require.EqualValues(t, 2, store.loads.Load(), "every read goes to the store")

	_, err = c.Role(verified(org, uuid.NewString()), org, user)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch, "store-only keeps the scope check")
	require.NoError(t, c.Invalidate(context.Background(), org, user))
	require.EqualValues(t, 1, evicted.Load(), "store-only still evicts what other replicas hold")
}

func TestConnectStopsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	shared := &unreachable{Memory: cache.NewMemory(), failures: 1 << 30}
	_, err := membership.Connect(ctx, shared, newCountingStore(), membership.Retry{Attempts: 3, Wait: time.Hour, AttemptTimeout: time.Second})
	require.ErrorIs(t, err, context.Canceled)
}
