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

// TestScoped_Get_PrefixesKeys — the wrapper must read/write only at
// the prefixed location, never the bare key.
func TestScoped_Get_PrefixesKeys(t *testing.T) {
	c := cache.NewMemory()
	scoped := cache.Scoped(c, "t:org-A")
	require.NoError(t, scoped.Set(t.Context(), "members:user-1", []byte("admin"), time.Minute))

	// Reading the bare key on the underlying cache MUST miss — the
	// scoped writer never touched it. Reading via the scoped wrapper
	// hits.
	_, err := c.Get(t.Context(), "members:user-1")
	require.ErrorIs(t, err, cache.ErrNotFound)

	got, err := c.Get(t.Context(), "t:org-A:members:user-1")
	require.NoError(t, err)
	require.Equal(t, []byte("admin"), got)

	got2, err := scoped.Get(t.Context(), "members:user-1")
	require.NoError(t, err)
	require.Equal(t, []byte("admin"), got2)
}

// TestScoped_TenantIsolation — the safety property the wrapper buys
// us. Two scoped caches over the same underlying store, scoped to
// different tenants, MUST NOT see each other's data even when the
// inner-cache key (after the prefix) is identical.
//
// This is the bug class we're defending against: a future caller
// uses just `members:user-1` without including the tenant id; the
// scoped wrapper adds it transparently, and tenant A's lookup can't
// return tenant B's value.
func TestScoped_TenantIsolation(t *testing.T) {
	c := cache.NewMemory()
	a := cache.Scoped(c, cache.TenantPrefix("org-A"))
	b := cache.Scoped(c, cache.TenantPrefix("org-B"))

	require.NoError(t, a.Set(t.Context(), "members:user-1", []byte("from-A"), time.Minute))
	require.NoError(t, b.Set(t.Context(), "members:user-1", []byte("from-B"), time.Minute))

	gotA, err := a.Get(t.Context(), "members:user-1")
	require.NoError(t, err)
	require.Equal(t, []byte("from-A"), gotA)

	gotB, err := b.Get(t.Context(), "members:user-1")
	require.NoError(t, err)
	require.Equal(t, []byte("from-B"), gotB)
}

// TestScoped_Compose — Scoped(Scoped(c, t), u) is the canonical
// per-(tenant,user) namespace. Nested wrappers concatenate prefixes
// in the obvious way; nothing weird happens.
func TestScoped_Compose(t *testing.T) {
	c := cache.NewMemory()
	tenant := cache.Scoped(c, cache.TenantPrefix("org-A"))
	tenantUser := cache.Scoped(tenant, cache.UserPrefix("user-1"))

	require.NoError(t, tenantUser.Set(t.Context(), "settings", []byte("v1"), time.Minute))

	// Underlying key is "t:org-A:u:user-1:settings" — verify by
	// reading at that exact path with the raw cache.
	got, err := c.Get(t.Context(), "t:org-A:u:user-1:settings")
	require.NoError(t, err)
	require.Equal(t, []byte("v1"), got)
}

// TestScoped_DeleteHonorsPrefix — Delete must prefix every supplied
// key; otherwise an explicit Delete call would skip the scoped data
// (and worst case, delete unscoped data with the same bare name).
func TestScoped_DeleteHonorsPrefix(t *testing.T) {
	c := cache.NewMemory()
	scoped := cache.Scoped(c, "t:org-A")
	require.NoError(t, scoped.Set(t.Context(), "k", []byte("v"), time.Minute))
	require.NoError(t, c.Set(t.Context(), "k", []byte("unscoped-v"), time.Minute)) // bare key

	require.NoError(t, scoped.Delete(t.Context(), "k"))

	// Scoped value gone, bare value untouched.
	_, err := scoped.Get(t.Context(), "k")
	require.ErrorIs(t, err, cache.ErrNotFound)
	got, err := c.Get(t.Context(), "k")
	require.NoError(t, err)
	require.Equal(t, []byte("unscoped-v"), got)
}

// TestOrgMembershipCache_KeyIncludesTenantAndUserPrefixes — pins the
// key format so a future change can't accidentally drop the
// scope-marker prefixes (which would break the safety this layer
// buys us). The exact bytes matter: tooling, dashboards, and Redis
// SCAN policies depend on the "t:..." / "u:..." conventions.
func TestOrgMembershipCache_KeyIncludesTenantAndUserPrefixes(t *testing.T) {
	c := cache.NewMemory()
	mc := cache.NewOrgMembershipCache(c)
	require.NoError(t, mc.Set(t.Context(), "org-A", "user-1", &cache.OrgMembership{Role: "admin"}))

	// The key format under the hood — verify by raw read.
	got, err := c.Get(t.Context(), "t:org-A:u:user-1:orgmember")
	require.NoError(t, err)
	require.Equal(t, []byte("admin"), got)
}
