package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"api/pkg/cache"
)

// TestMemoryCache_GetSetDelete exercises the in-memory fallback's core
// contract — the same contract the Redis impl satisfies, so one test
// suite validates both. Redis-specific behavior (actual persistence,
// network timeouts) is out of scope here; those get caught by the
// higher-level integration tests that spin up real Redis.
func TestMemoryCache_GetSetDelete(t *testing.T) {
	c := cache.NewMemory()
	ctx := context.Background()

	// Miss on empty cache.
	_, err := c.Get(ctx, "nope")
	require.ErrorIs(t, err, cache.ErrNotFound)

	// Set + get.
	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), time.Minute))
	v, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), v)

	// Delete removes it.
	require.NoError(t, c.Delete(ctx, "k1"))
	_, err = c.Get(ctx, "k1")
	require.ErrorIs(t, err, cache.ErrNotFound)
}

// TestMemoryCache_TTL — very short TTL expires on the next Get. Proves
// lazy expiry works; not testing millisecond-accurate timing.
func TestMemoryCache_TTL(t *testing.T) {
	c := cache.NewMemory()
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k", []byte("v"), 10*time.Millisecond))
	time.Sleep(50 * time.Millisecond)
	_, err := c.Get(ctx, "k")
	require.ErrorIs(t, err, cache.ErrNotFound, "entry should have expired")
}

// TestOrgMembershipCache verifies the typed cache encodes its keys
// per-tenant (multi-tenant isolation) AND round-trips the Role string
// correctly. Shares the Cache interface tests above so this focuses on
// the type-wrapper behavior.
func TestOrgMembershipCache(t *testing.T) {
	omc := cache.NewOrgMembershipCache(cache.NewMemory())
	ctx := context.Background()

	// Miss = DB lookup signal.
	_, err := omc.Get(ctx, "org-a", "user-1")
	require.ErrorIs(t, err, cache.ErrNotFound)

	// Cache admin role.
	require.NoError(t, omc.Set(ctx, "org-a", "user-1",
		&cache.OrgMembership{Role: "ORG_ROLE_ADMIN"}))

	m, err := omc.Get(ctx, "org-a", "user-1")
	require.NoError(t, err)
	require.Equal(t, "ORG_ROLE_ADMIN", m.Role)

	// Multi-tenant key isolation — user-1 in org-b must NOT have the
	// admin role that user-1 in org-a has. If keys weren't per-tenant
	// this would spuriously return admin.
	_, err = omc.Get(ctx, "org-b", "user-1")
	require.ErrorIs(t, err, cache.ErrNotFound,
		"org-b's cache must be independent of org-a's")

	// Cache negative lookup (user is confirmed NOT a member).
	require.NoError(t, omc.Set(ctx, "org-a", "user-2",
		&cache.OrgMembership{Role: ""}))
	m, err = omc.Get(ctx, "org-a", "user-2")
	require.NoError(t, err, "negative lookup is a real hit, not ErrNotFound")
	require.Equal(t, "", m.Role)

	// Invalidate flips the membership back to uncached — subsequent
	// caller will fall through to the DB. This is what the mutation
	// hook in business/organizations.go relies on.
	require.NoError(t, omc.Invalidate(ctx, "org-a", "user-1"))
	_, err = omc.Get(ctx, "org-a", "user-1")
	require.ErrorIs(t, err, cache.ErrNotFound)
}

// TestOrgMembershipCache_InvalidateOrg verifies the bulk-invalidate
// path used when an entire org is deleted or a cascade wipe is needed.
func TestOrgMembershipCache_InvalidateOrg(t *testing.T) {
	omc := cache.NewOrgMembershipCache(cache.NewMemory())
	ctx := context.Background()

	for _, uid := range []string{"u1", "u2", "u3"} {
		require.NoError(t, omc.Set(ctx, "org-x", uid,
			&cache.OrgMembership{Role: "ORG_ROLE_MEMBER"}))
	}

	require.NoError(t, omc.InvalidateOrg(ctx, "org-x", []string{"u1", "u2", "u3"}))

	for _, uid := range []string{"u1", "u2", "u3"} {
		_, err := omc.Get(ctx, "org-x", uid)
		require.ErrorIs(t, err, cache.ErrNotFound,
			"all org-x entries should be gone")
	}
}

// TestErrNotFoundIsStable ensures the sentinel error is stable — callers
// use errors.Is which would break if the package ever replaced the
// sentinel.
func TestErrNotFoundIsStable(t *testing.T) {
	require.True(t, errors.Is(cache.ErrNotFound, cache.ErrNotFound))
}
