//go:build !pure

package redisintegration_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/infra"
	"accounts/pkg/membership"
)

// countingStore is the real store, counting the membership reads that reach it.
// delay widens the window in which concurrent misses overlap.
type countingStore struct {
	loads atomic.Int64
	delay time.Duration
}

func (s *countingStore) GetOrgMembership(ctx context.Context, orgID, userID string) (*gen.OrgMembership, error) {
	s.loads.Add(1)
	time.Sleep(s.delay)
	return testStore.GetOrgMembership(ctx, orgID, userID)
}

// instance is one accounts replica's membership cache: its own Redis client
// (so its own tracking connection) and its own in-process tier, over the one
// real Redis server and the one real store.
func instance(t *testing.T, store membership.Store) (*membership.Cache, *goredis.Client) {
	t.Helper()
	client, err := infra.NewRedisClient(testCtx)
	require.NoError(t, err)
	require.NotNil(t, client, "the cache dependency must be wired in this suite")
	t.Cleanup(func() { _ = client.Close() })
	c, err := membership.New(testCtx, infra.MembershipCacheLayer(client), store)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c, client
}

func seedUser(t *testing.T) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.As(business.System()).Within(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared "tx" key
		_, err := tx.Exec(ctx,
			`INSERT INTO users (uuid, primary_email, status) VALUES ($1, $2, 'active')`,
			id, fmt.Sprintf("user-%s@test.local", id))
		return err
	}))
	return id
}

func seedOrg(t *testing.T, ownerID string) string {
	t.Helper()
	id := business.NewIDString()
	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		tx := ctx.Value("tx").(pgx.Tx) //nolint:staticcheck // shared key with WithControlPlane
		_, err := tx.Exec(ctx,
			`INSERT INTO organizations (id, name, slug, owner_id) VALUES ($1, 'Cache Org', $2, $3)`,
			id, "org-"+id, ownerID)
		return err
	}))
	return id
}

// setRole writes the membership row the way the business layer does, without
// the business layer's invalidation: each test decides who invalidates.
func setRole(t *testing.T, orgID, userID, role string) {
	t.Helper()
	require.NoError(t, testStore.As(business.Identity{OrgID: orgID}).AddOrgMember(testCtx, userID, role))
}

func memberOf(orgID, userID string) context.Context {
	return auth.WithVerifiedDatabaseIdentity(testCtx, userID, orgID)
}

// Acceptance (#927): two service instances load a missing membership from the
// store once. Every read below starts at the same moment on a cold key, half
// on each instance; the store is slowed so they all overlap.
func TestTwoInstancesLoadAMissingMembershipFromTheStoreOnce(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	setRole(t, org, user, "member")
	store := &countingStore{delay: 200 * time.Millisecond}
	a, _ := instance(t, store)
	b, _ := instance(t, store)

	const perInstance = 8
	start := make(chan struct{})
	roles := make(chan string, 2*perInstance)
	errs := make(chan error, 2*perInstance)
	var wg sync.WaitGroup
	for _, c := range []*membership.Cache{a, b} {
		for range perInstance {
			wg.Go(func() {
				<-start
				role, err := c.Role(memberOf(org, user), org, user)
				roles <- role
				errs <- err
			})
		}
	}
	close(start)
	wg.Wait()
	close(roles)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for role := range roles {
		require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
	}
	require.EqualValues(t, 1, store.loads.Load(), "one load from the store across both instances")

	// Both instances keep serving it without the store.
	for _, c := range []*membership.Cache{a, b} {
		_, err := c.Role(memberOf(org, user), org, user)
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, store.loads.Load())
}

// Acceptance (#927): a membership change on one instance evicts the other's
// in-process copy. b holds the copy in its in-process tier; a changes the row
// and invalidates, as the business layer does after every committed membership
// mutation. b must see the change before its in-process copy could have
// expired on its own, which is only possible if a's invalidation reached b's
// process through Redis.
func TestMembershipChangeOnOneInstanceEvictsTheOthersInProcessCopy(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	setRole(t, org, user, "member")
	store := &countingStore{}
	a, _ := instance(t, store)
	b, _ := instance(t, store)

	role, err := b.Role(memberOf(org, user), org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
	warmedAt := time.Now()

	// The store changes; until someone invalidates, b serves its copy.
	setRole(t, org, user, "admin")
	role, err = b.Role(memberOf(org, user), org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role, "b serves its cached copy")

	require.NoError(t, a.Invalidate(testCtx, org, user))
	require.Eventually(t, func() bool {
		role, err := b.Role(memberOf(org, user), org, user)
		return err == nil && role == gen.OrgRole_ORG_ROLE_ADMIN.String()
	}, 3*time.Second, 10*time.Millisecond)
	// The in-process tier keeps a copy 5s, less at most 10% jitter.
	require.Less(t, time.Since(warmedAt), 4*time.Second,
		"b saw the change before its in-process copy expired: the invalidation evicted it")
}

// A removal is the case that matters for authorization: the departed member's
// cached role must stop being served on every instance, and a later re-add
// must replace the cached non-membership.
func TestRemovalAndReAddReachEveryInstance(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	user := seedUser(t)
	setRole(t, org, user, "admin")
	store := &countingStore{}
	a, _ := instance(t, store)
	b, _ := instance(t, store)

	for _, c := range []*membership.Cache{a, b} {
		role, err := c.Role(memberOf(org, user), org, user)
		require.NoError(t, err)
		require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN.String(), role)
	}

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.RemoveOrgMember(ctx, org, user)
	}))
	require.NoError(t, a.Invalidate(testCtx, org, user))
	require.Eventually(t, func() bool {
		role, err := b.Role(memberOf(org, user), org, user)
		return err == nil && role == ""
	}, 3*time.Second, 10*time.Millisecond, "b must stop serving the removed member's role")

	setRole(t, org, user, "member")
	require.NoError(t, b.Invalidate(testCtx, org, user))
	require.Eventually(t, func() bool {
		role, err := a.Role(memberOf(org, user), org, user)
		return err == nil && role == gen.OrgRole_ORG_ROLE_MEMBER.String()
	}, 3*time.Second, 10*time.Millisecond, "a must drop the cached non-membership")
}

// An unreachable Redis degrades reads to the store; it never fails them.
func TestUnreachableRedisFallsThroughToTheStore(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	setRole(t, org, user, "owner")
	store := &countingStore{}
	c, client := instance(t, store)

	require.NoError(t, client.Close())
	for range 3 {
		role, err := c.Role(memberOf(org, user), org, user)
		require.NoError(t, err)
		require.Equal(t, gen.OrgRole_ORG_ROLE_OWNER.String(), role)
	}
	require.GreaterOrEqual(t, store.loads.Load(), int64(1))
}

// unreachableClient is a client with the real connection's options, pointed at
// a port on the same host that was just released: nothing listens there, so
// every dial is refused, the way an unreachable cache is at boot.
func unreachableClient(t *testing.T) *goredis.Client {
	t.Helper()
	real, err := infra.NewRedisClient(testCtx)
	require.NoError(t, err)
	options := *real.Options()
	require.NoError(t, real.Close())
	host, _, err := net.SplitHostPort(options.Addr)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	require.NoError(t, err)
	options.Addr = listener.Addr().String()
	require.NoError(t, listener.Close())
	client := goredis.NewClient(&options)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// A cache that cannot be reached at boot: Connect retries within
// BootRetry, then runs store-only, and every read is the store's current
// answer — a removal is seen on the next check, with no invalidation at all.
func TestUnreachableRedisAtBootRunsStoreOnlyAndIsNeverStale(t *testing.T) {
	owner := seedUser(t)
	org := seedOrg(t, owner)
	user := seedUser(t)
	setRole(t, org, user, "admin")
	store := &countingStore{}
	client := unreachableClient(t)

	started := time.Now()
	c, err := membership.Connect(testCtx, infra.MembershipCacheLayer(client), store, membership.BootRetry,
		membership.WithEviction(infra.LegacyMembershipKeyEvictor(client)))
	require.NoError(t, err, "an unreachable cache is not a boot failure")
	t.Cleanup(c.Close)
	require.Error(t, c.StoreOnly())
	require.ErrorContains(t, c.StoreOnly(), "attempt 5 of 5")
	require.Less(t, time.Since(started), 15*time.Second, "the retry is bounded")

	role, err := c.Role(memberOf(org, user), org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_ADMIN.String(), role)

	require.NoError(t, testStore.WithControlPlane(testCtx, func(ctx context.Context) error {
		return testStore.RemoveOrgMember(ctx, org, user)
	}))
	role, err = c.Role(memberOf(org, user), org, user)
	require.NoError(t, err)
	require.Empty(t, role, "store-only sees the removal on the next check")

	setRole(t, org, user, "member")
	role, err = c.Role(memberOf(org, user), org, user)
	require.NoError(t, err)
	require.Equal(t, gen.OrgRole_ORG_ROLE_MEMBER.String(), role)
	require.EqualValues(t, 3, store.loads.Load(), "every read went to the store")

	_, err = c.Role(memberOf(org, seedUser(t)), org, user)
	require.ErrorIs(t, err, auth.ErrVerifiedDatabaseScopeMismatch)
}

// Rollout shim (#927): an invalidation on a new replica deletes the key the
// previous release's replicas read, so their copies go too.
func TestInvalidationEvictsThePreviousReleasesKey(t *testing.T) {
	user := seedUser(t)
	org := seedOrg(t, user)
	client := stateClient(t)
	c, err := membership.New(testCtx, infra.MembershipCacheLayer(client), &countingStore{},
		membership.WithEviction(infra.LegacyMembershipKeyEvictor(client)))
	require.NoError(t, err)
	t.Cleanup(c.Close)

	legacy := "t:" + org + ":u:" + user + ":orgmember"
	require.NoError(t, client.Set(testCtx, legacy, "ORG_ROLE_ADMIN", 30*time.Second).Err())
	require.NoError(t, c.Invalidate(testCtx, strings.ToUpper(org), user))
	n, err := client.Exists(testCtx, legacy).Result()
	require.NoError(t, err)
	require.Zero(t, n, "the previous release's copy is gone")
}
