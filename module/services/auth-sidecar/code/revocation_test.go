package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeRevoker is a controllable revoker for Check-level tests.
type fakeRevoker struct {
	revoked map[string]bool
	err     error
}

func (f *fakeRevoker) Revoked(_ context.Context, jti string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.revoked[jti], nil
}

// fakeStore stands in for the shared Redis revocation set. It counts lookups so
// tests can assert the local cache actually collapses store round-trips.
type fakeStore struct {
	mu      sync.Mutex
	entries map[string]bool
	err     error
	calls   int
}

func newFakeStore() *fakeStore { return &fakeStore{entries: map[string]bool{}} }

func (f *fakeStore) revoked(_ context.Context, jti string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.entries[jti], nil
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

// E2E-shaped proof through Check: valid token → 200, then the jti is added to
// the shared revocation set (logout / admin-kill), and immediate reuse → 401.
// This is the gap the issue describes: on the sidecar path a logged-out access
// token used to keep working until natural expiry.
func TestUnit_Revocation_GatewayPath_LogoutThenReuseRejected(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	token := signClaims(t, priv, c)

	store := newFakeStore()
	// ttl=0 disables the local cache so the second check observes the
	// revocation immediately — matching the "immediately reuse → 401" criterion.
	s.revoker = newCachedRevoker(store.revoked, 0)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "protected RPC succeeds before logout")

	store.mark(c.ID) // accounts revokes the access-token jti on logout.

	resp, err = s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "reused post-logout token must be rejected")
	require.Equal(t, int32(401), int32(resp.GetDeniedResponse().Status.Code))
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

// A token with no jti can't be revoked (nothing to key on) and must still pass
// once signature/claims are valid — the revoker is never consulted.
func TestUnit_Revocation_NoJTI_Allowed(t *testing.T) {
	s, priv := newTestSidecar(t)
	s.revoker = &fakeRevoker{err: errors.New("must not be called")}
	c := validClaims(time.Now())
	c.ID = ""
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", bearer(token)))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())
}

// ============================================================================
// cachedRevoker: hot-path caching, bounded staleness window, no error caching.
// ============================================================================

func TestUnit_CachedRevoker_HotPathAndBoundedWindow(t *testing.T) {
	store := newFakeStore()
	now := time.Now()
	rev := newCachedRevoker(store.revoked, 3*time.Second)
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
	rev := newCachedRevoker(store.revoked, 5*time.Second)

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
	rev := newCachedRevoker(store.revoked, 5*time.Second)

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
