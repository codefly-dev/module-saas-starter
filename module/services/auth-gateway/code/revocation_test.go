package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// fakeRevoker is a controllable revoker for Check-level tests.
type fakeRevoker struct {
	revoked        map[string]bool
	sessionRevoked map[string]bool
	err            error
}

func (f *fakeRevoker) Revoked(_ context.Context, jti string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[jti], nil
}

func (f *fakeRevoker) RevokedSession(_ context.Context, sessionID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.sessionRevoked[sessionID], nil
}

func (f *fakeRevoker) Forget(string) {}

// fakeStore stands in for the shared Redis revocation set. It counts lookups so
// tests can assert the local cache actually collapses store round-trips.
type fakeStore struct {
	mu       sync.Mutex
	entries  map[string]bool
	sessions map[string]bool
	err      error
	calls    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{entries: map[string]bool{}, sessions: map[string]bool{}}
}

func (f *fakeStore) revoked(_ context.Context, jti string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.entries[jti], nil
}

func (f *fakeStore) revokedSession(_ context.Context, sessionID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.sessions[sessionID], nil
}

func (f *fakeStore) mark(jti string) {
	f.mu.Lock()
	f.entries[jti] = true
	f.mu.Unlock()
}

func bearer(token string) map[string]string {
	return map[string]string{"authorization": "Bearer " + token}
}

// ============================================================================
// Acceptance: a revoked jti is rejected on the gateway path (checkJWT).
// ============================================================================

func TestUnit_RevokedJWT_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	s.revoker = &fakeRevoker{revoked: map[string]bool{c.ID: true}}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "a revoked jti must be denied")
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
}

func TestUnit_NotRevokedJWT_Allowed(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	s.revoker = &fakeRevoker{revoked: map[string]bool{"some-other-jti": true}}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "an un-revoked jti must pass")
}

// E2E-shaped proof through Check, with the DEFAULT cache TTL: protected RPC →
// 200, logout through the gateway (which itself carries the token), accounts
// revokes the jti, immediate reuse → 401. The logout request must not shield
// the replay behind the "not revoked" answer it just cached — checkJWT forgets
// the jti's cache entry on logout routes.
func TestUnit_Revocation_GatewayPath_LogoutThenReuseRejected(t *testing.T) {
	for _, logoutPath := range []string{
		"/v1/auth/logout",
		"/saas.accounts.v1.AuthService/Logout",
		"/customers.AuthService/Logout",
	} {
		t.Run(logoutPath, func(t *testing.T) {
			s, priv := newTestSidecar(t)
			c := validClaims(time.Now())
			token := signClaims(t, priv, c)

			store := newFakeStore()
			s.revoker = newCachedRevoker(store.revoked, store.revokedSession, defaultRevocationCacheTTL)

			resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
			require.NoError(t, err)
			require.NotNil(t, resp.GetOkResponse(), "protected RPC succeeds before logout")

			resp, err = s.Check(context.Background(), checkReq(logoutPath, bearer(token)))
			require.NoError(t, err)
			require.NotNil(t, resp.GetOkResponse(), "logout request itself is authorized")

			store.mark(c.ID) // accounts revokes the access-token jti on logout.

			resp, err = s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
			require.NoError(t, err)
			require.NotNil(t, resp.GetDeniedResponse(), "reused post-logout token must be rejected")
			require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
		})
	}
}

// A non-logout request leaves the cache intact — the replay is served from the
// documented ≤TTL window, not a store round-trip per request.
func TestUnit_Revocation_NonLogoutPathKeepsCache(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	token := signClaims(t, priv, c)

	store := newFakeStore()
	s.revoker = newCachedRevoker(store.revoked, store.revokedSession, defaultRevocationCacheTTL)

	for i := 0; i < 3; i++ {
		resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
		require.NoError(t, err)
		require.NotNil(t, resp.GetOkResponse())
	}
	// The first request round-trips both revocation keys (jti + sid) once each;
	// every repeat within the window is served entirely from the local cache.
	require.Equal(t, 2, store.calls, "repeat requests within the window hit the cache")
}

// ============================================================================
// Failure mode on store error is explicit: fail-closed by default, fail-open
// only when configured.
// ============================================================================

func TestUnit_Revocation_StoreError_FailClosedByDefault(t *testing.T) {
	s, priv := newTestSidecar(t)
	s.revocationFailOpen = false
	s.revoker = &fakeRevoker{err: errors.New("redis unreachable")}
	token := signClaims(t, priv, validClaims(time.Now()))

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "store error must deny in strict mode")
	require.Equal(t, int32(503), int32(resp.GetDeniedResponse().Status.Code))
}

func TestUnit_Revocation_StoreError_FailOpenWhenConfigured(t *testing.T) {
	s, priv := newTestSidecar(t)
	s.revocationFailOpen = true
	s.revoker = &fakeRevoker{err: errors.New("redis unreachable")}
	token := signClaims(t, priv, validClaims(time.Now()))

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "fail-open admits when explicitly configured")
}

// A token carrying neither a jti nor a sid can't be revoked (nothing to key on)
// and must still pass once signature/claims are valid — the revoker is never
// consulted on either path.
func TestUnit_Revocation_NoJTI_Allowed(t *testing.T) {
	s, priv := newTestSidecar(t)
	s.revoker = &fakeRevoker{err: errors.New("must not be called")}
	c := validClaims(time.Now())
	c.ID = ""
	c.SessionID = ""
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())
}

// Admin session-kill: the victim's outstanding access token has an un-revoked
// jti, but its `sid` is marked. checkJWT must deny on the session marker alone,
// covering the gateway path where the admin never held the token.
func TestUnit_SessionRevokedJWT_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	s.revoker = &fakeRevoker{sessionRevoked: map[string]bool{c.SessionID: true}}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "a killed session must be denied on the sid marker")
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
}

// A token that expired 30s ago is inside the 60s clock-skew leeway and must
// still be accepted — the bare literal WithLeeway(60) was 60 NANOSECONDS and
// would reject it, spuriously 401-ing clock-skewed tokens near expiry.
func TestUnit_ClockSkewLeeway_AcceptsRecentlyExpiredWithinWindow(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-30 * time.Second))
	s.revoker = &fakeRevoker{}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "a token expired within the leeway window must still be accepted")
}

// The revocation marker outlives the leeway window (accounts writes exp+leeway),
// so a token revoked while still inside [exp, exp+60s] is denied, not admitted.
func TestUnit_ClockSkewLeeway_RevokedWithinWindowStillDenied(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-30 * time.Second))
	s.revoker = &fakeRevoker{revoked: map[string]bool{c.ID: true}}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "a revoked token in the leeway window must be denied")
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
}

// The session-kill failure mode matches the jti path: a store error denies in
// strict mode rather than admitting a possibly-killed session.
func TestUnit_SessionRevocation_StoreError_FailClosedByDefault(t *testing.T) {
	s, priv := newTestSidecar(t)
	s.revocationFailOpen = false
	// jti path clean, session path errors — isolates the session check's stance.
	store := newFakeStore()
	store.err = errors.New("redis unreachable")
	s.revoker = newCachedRevoker(
		func(context.Context, string) (bool, error) { return false, nil },
		store.revokedSession,
		defaultRevocationCacheTTL,
	)
	token := signClaims(t, priv, validClaims(time.Now()))

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "session store error must deny in strict mode")
	require.Equal(t, int32(503), int32(resp.GetDeniedResponse().Status.Code))
}

// A burst of distinct still-live keys (frozen clock, so nothing expires) must
// not grow the cache without bound: when the expired-sweep frees nothing, the
// oldest batch is hard-evicted so the map stays at or below the cap.
func TestUnit_CachedRevoker_HardEvictsWhenSweepFreesNothing(t *testing.T) {
	now := time.Now()
	noStore := func(context.Context, string) (bool, error) { return false, nil }
	rev := newCachedRevoker(noStore, noStore, time.Minute)
	rev.now = func() time.Time { return now } // frozen: no entry ever expires

	for i := 0; i < revocationCacheMaxEntries+revocationCacheEvictBatch; i++ {
		rev.store("k"+strconv.Itoa(i), false)
	}

	require.LessOrEqual(t, len(rev.entries), revocationCacheMaxEntries,
		"cache must stay bounded even when no entry has expired")
}

// ============================================================================
// cachedRevoker: hot-path caching, bounded staleness window, no error caching.
// ============================================================================

func TestUnit_CachedRevoker_HotPathAndBoundedWindow(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	rev := newCachedRevoker(store.revoked, store.revokedSession, 3*time.Second)
	rev.now = func() time.Time { return now }

	// First lookup hits the store and caches "not revoked".
	got, err := rev.Revoked(context.Background(), "jti-1")
	require.NoError(t, err)
	require.False(t, got)
	require.Equal(t, 1, store.calls)

	// Within the window the cached answer is served — no extra store round-trip,
	// even though the token was just revoked in Redis. This is the documented
	// ≤TTL window.
	store.mark("jti-1")
	got, err = rev.Revoked(context.Background(), "jti-1")
	require.NoError(t, err)
	require.False(t, got, "cached not-revoked answer served within the window")
	require.Equal(t, 1, store.calls)

	// After the window elapses the store is consulted again and the revocation
	// is observed.
	now = now.Add(3 * time.Second)
	got, err = rev.Revoked(context.Background(), "jti-1")
	require.NoError(t, err)
	require.True(t, got)
	require.Equal(t, 2, store.calls)
}

func TestUnit_CachedRevoker_CachesRevokedAnswer(t *testing.T) {
	store := newFakeStore()
	store.mark("jti-2")
	rev := newCachedRevoker(store.revoked, store.revokedSession, 5*time.Second)

	for i := 0; i < 3; i++ {
		got, err := rev.Revoked(context.Background(), "jti-2")
		require.NoError(t, err)
		require.True(t, got)
	}
	require.Equal(t, 1, store.calls, "repeated revoked lookups collapse to one store hit")
}

func TestUnit_CachedRevoker_DoesNotCacheErrors(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis down")
	rev := newCachedRevoker(store.revoked, store.revokedSession, 5*time.Second)

	_, err := rev.Revoked(context.Background(), "jti-3")
	require.Error(t, err)
	require.Equal(t, 1, store.calls)

	// Store recovers: the next call must re-consult it (the error was not cached).
	store.err = nil
	got, err := rev.Revoked(context.Background(), "jti-3")
	require.NoError(t, err)
	require.False(t, got)
	require.Equal(t, 2, store.calls)
}

func TestUnit_NoopRevoker_NeverRevokes(t *testing.T) {
	got, err := noopRevoker{}.Revoked(context.Background(), "anything")
	require.NoError(t, err)
	require.False(t, got)
}
