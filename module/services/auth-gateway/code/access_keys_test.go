package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// staticAccessKeys pins a fixed set of verification keys, standing in for the
// JWKS-backed resolver in tests that are not about key acquisition. Keys are
// addressed by the same deterministic kid accounts derives, so a token signed
// in a test and stamped with accessKeyID verifies here.
func staticAccessKeys(keys ...ed25519.PublicKey) accessKeys {
	set := make(map[string]ed25519.PublicKey, len(keys))
	for _, key := range keys {
		set[accessKeyID(key)] = key
	}
	return fixedAccessKeys(set)
}

type fixedAccessKeys map[string]ed25519.PublicKey

func (f fixedAccessKeys) keyFor(_ context.Context, keyID string) (ed25519.PublicKey, error) {
	return accessKeyFor(f, keyID)
}

func (f fixedAccessKeys) loaded() bool { return len(f) > 0 }

// unavailableAccessKeys stands in for a gateway that has not managed to fetch
// the published key set at all.
type unavailableAccessKeys struct{}

func (unavailableAccessKeys) keyFor(context.Context, string) (ed25519.PublicKey, error) {
	return nil, fmt.Errorf("%w: test", errNoVerificationKeys)
}

func (unavailableAccessKeys) loaded() bool { return false }

// accessKeyID mirrors the kid accounts derives in pkg/auth/ed25519 — the first
// eight bytes of SHA-256 over the public key — so tests mint tokens carrying
// the same key id the published JWKS advertises.
func accessKeyID(pub ed25519.PublicKey) string {
	digest := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(digest[:8])
}

func signAccessToken(t *testing.T, priv ed25519.PrivateKey, keyID string, claims accessClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	if keyID != "" {
		token.Header["kid"] = keyID
	}
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func accessJWKSFor(t *testing.T, keys ...ed25519.PublicKey) string {
	t.Helper()
	document := make(map[string]ed25519.PublicKey, len(keys))
	for _, key := range keys {
		document[accessKeyID(key)] = key
	}
	return jwksDocument(document)
}

// ---------------------------------------------------------------------------
// Key selection policy
// ---------------------------------------------------------------------------

func TestAccessKeyFor_UnknownKeyIDSelectsNothing(t *testing.T) {
	first, _ := mustEd25519(t)
	second, _ := mustEd25519(t)
	keys := map[string]ed25519.PublicKey{accessKeyID(first): first, accessKeyID(second): second}

	_, err := accessKeyFor(keys, "not-a-published-key")
	require.ErrorIs(t, err, errUnknownKeyID)
}

func TestAccessKeyFor_MissingKeyIDUsesTheSolePublishedKey(t *testing.T) {
	only, _ := mustEd25519(t)
	key, err := accessKeyFor(map[string]ed25519.PublicKey{accessKeyID(only): only}, "")
	require.NoError(t, err)
	require.Equal(t, only, key)
}

func TestAccessKeyFor_MissingKeyIDIsAmbiguousDuringOverlap(t *testing.T) {
	current, _ := mustEd25519(t)
	retiring, _ := mustEd25519(t)
	keys := map[string]ed25519.PublicKey{
		accessKeyID(current):  current,
		accessKeyID(retiring): retiring,
	}

	_, err := accessKeyFor(keys, "")
	require.ErrorIs(t, err, errAmbiguousKeyID)
}

// ---------------------------------------------------------------------------
// Overlap, rotation, and restart
// ---------------------------------------------------------------------------

// rotatingJWKSServer serves a JWKS document that the test can swap, counting
// fetches so bounds can be asserted.
type rotatingJWKSServer struct {
	server   *httptest.Server
	document atomic.Pointer[string]
	hits     int64
}

func newRotatingJWKSServer(t *testing.T, document string) *rotatingJWKSServer {
	t.Helper()
	rotating := &rotatingJWKSServer{}
	rotating.document.Store(&document)
	rotating.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&rotating.hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*rotating.document.Load()))
	}))
	t.Cleanup(rotating.server.Close)
	return rotating
}

func (r *rotatingJWKSServer) publish(document string) { r.document.Store(&document) }

func (r *rotatingJWKSServer) fetches() int64 { return atomic.LoadInt64(&r.hits) }

func TestAccessJWKS_AcceptsBothKeysDuringOverlapWithoutRestart(t *testing.T) {
	firstPub, firstPriv := mustEd25519(t)
	secondPub, secondPriv := mustEd25519(t)

	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, firstPub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)

	requireAdmitted(t, authz, signAccessToken(t, firstPriv, accessKeyID(firstPub), validClaims(time.Now())))

	// accounts publishes the incoming key alongside the outgoing one and starts
	// signing with it. The gateway is not restarted.
	publisher.publish(accessJWKSFor(t, secondPub, firstPub))

	requireAdmitted(t, authz, signAccessToken(t, secondPriv, accessKeyID(secondPub), validClaims(time.Now())))
	requireAdmitted(t, authz, signAccessToken(t, firstPriv, accessKeyID(firstPub), validClaims(time.Now())))
}

func TestAccessJWKS_RestartMidOverlapAcceptsBothOrderings(t *testing.T) {
	currentPub, currentPriv := mustEd25519(t)
	retiringPub, retiringPriv := mustEd25519(t)

	for name, document := range map[string]string{
		"current first":  accessJWKSFor(t, currentPub, retiringPub),
		"retiring first": accessJWKSFor(t, retiringPub, currentPub),
	} {
		t.Run(name, func(t *testing.T) {
			publisher := newRotatingJWKSServer(t, document)
			// A freshly started gateway: nothing cached, both keys must work.
			authz := newJWKSExtAuthz(t, publisher.server.URL)

			requireAdmitted(t, authz, signAccessToken(t, currentPriv, accessKeyID(currentPub), validClaims(time.Now())))
			requireAdmitted(t, authz, signAccessToken(t, retiringPriv, accessKeyID(retiringPub), validClaims(time.Now())))
		})
	}
}

func TestAccessJWKS_RetiredKeyIsRejectedAfterConvergence(t *testing.T) {
	currentPub, _ := mustEd25519(t)
	retiredPub, retiredPriv := mustEd25519(t)

	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, currentPub, retiredPub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	clock := newTestClock(t, authz)

	retiredToken := signAccessToken(t, retiredPriv, accessKeyID(retiredPub), validClaims(time.Now()))
	requireAdmitted(t, authz, retiredToken)

	// accounts drops the retired key. The gateway converges within one cache
	// TTL; the stale grace only applies while the publisher is unreachable.
	publisher.publish(accessJWKSFor(t, currentPub))
	clock.advance(accessJWKSCacheTTL + time.Second)

	requireDenied(t, authz, retiredToken, 401)
}

// ---------------------------------------------------------------------------
// Startup outage and recovery
// ---------------------------------------------------------------------------

func TestAccessJWKS_StartupOutageRecoversWithoutRestart(t *testing.T) {
	pub, priv := mustEd25519(t)

	var reachable atomic.Bool
	document := accessJWKSFor(t, pub)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !reachable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(server.Close)

	authz := newJWKSExtAuthz(t, server.URL)
	clock := newTestClock(t, authz)
	gateway := NewGateway(authz, NewRouteMatcher(testRouteEntries(), nil),
		map[string]*url.URL{"accounts": MustURL(server.URL), "frontend": MustURL(server.URL)}, nil,
		newFakeSolutionRegistry())

	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))
	requireDenied(t, authz, token, 503)
	require.False(t, authz.hasLoadedAccessTokenKeys())
	require.Equal(t, http.StatusServiceUnavailable, readyStatus(t, gateway))

	// accounts finishes starting. No gateway restart, no operator action.
	reachable.Store(true)
	clock.advance(jwksFailureBackoff)

	requireAdmitted(t, authz, token)
	require.True(t, authz.hasLoadedAccessTokenKeys())
	require.Equal(t, http.StatusOK, readyStatus(t, gateway))
}

func TestAccessJWKS_CachedKeysSurviveAPublisherOutage(t *testing.T) {
	pub, priv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	clock := newTestClock(t, authz)

	requireAdmitted(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	publisher.server.Close()

	// Within the documented grace the cached key set still verifies, so an
	// accounts restart does not take authentication down with it.
	clock.advance(accessJWKSCacheTTL + time.Minute)
	requireAdmitted(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	require.True(t, authz.hasLoadedAccessTokenKeys())

	// Past it the gateway fails closed: an unreachable publisher must not keep
	// a withdrawn key alive indefinitely.
	clock.advance(accessJWKSStaleGrace)
	requireDenied(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
}

// TestAccessJWKS_StaleKeysDoNotWithdrawTheListener pins the scope of the
// readiness signal. A key set aged past its grace stops verifying tokens, but
// the gateway keeps reporting ready, because the routes that never carried a
// token — the billing and email webhooks — must keep being served through an
// accounts JWKS outage. Only a gateway that has never loaded keys is unready.
func TestAccessJWKS_StaleKeysDoNotWithdrawTheListener(t *testing.T) {
	pub, priv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	clock := newTestClock(t, authz)
	gateway := NewGateway(authz, NewRouteMatcher(testRouteEntries(), nil),
		map[string]*url.URL{
			"accounts": MustURL(publisher.server.URL),
			"frontend": MustURL(publisher.server.URL),
		}, nil, newFakeSolutionRegistry())

	requireAdmitted(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	publisher.server.Close()
	clock.advance(accessJWKSCacheTTL + accessJWKSStaleGrace + time.Minute)

	requireDenied(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
	require.True(t, authz.hasLoadedAccessTokenKeys(),
		"a gateway that loaded keys once must not report itself unconfigured because they aged")

	// The upstream is unreachable here too, so readiness fails on that — the
	// more actionable reason — rather than on key staleness. What matters is
	// that the key age is not itself a reason.
	publisher2 := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	gateway.upstreams["accounts"] = MustURL(publisher2.server.URL)
	gateway.upstreams["frontend"] = MustURL(publisher2.server.URL)
	require.Equal(t, http.StatusOK, readyStatus(t, gateway),
		"stale keys must not withdraw the listener from public routes")
}

// ---------------------------------------------------------------------------
// Bounded fetching under untrusted input
// ---------------------------------------------------------------------------

func TestAccessJWKS_ConcurrentUnknownKeyIDsAreBounded(t *testing.T) {
	pub, priv := mustEd25519(t)
	_, attacker := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	// The bound is one probe per interval, so the window has to be held still
	// for the count to mean anything.
	clock := newTestClock(t, authz)

	// Ordinary traffic warms the cache first, so what the storm adds is the
	// probe budget alone rather than a cold fetch the scheduler may or may not
	// have folded into it.
	requireAdmitted(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	warm := publisher.fetches()
	require.Equal(t, int64(1), warm)

	// Tokens are minted on this goroutine: the callers exercise key resolution,
	// and require's FailNow is only valid here.
	storm := func(round int) {
		const callers = 32
		tokens := make([]string, callers)
		for i := range tokens {
			tokens[i] = signAccessToken(t, attacker,
				fmt.Sprintf("attacker-chosen-%d-%d", round, i), validClaims(time.Now()))
		}

		statuses := make([]int32, callers)
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				statuses[i] = deniedStatus(authz, tokens[i])
			}(i)
		}
		wg.Wait()

		for i, status := range statuses {
			require.Equal(t, int32(401), status, "caller %d must be denied as a bad credential", i)
		}
	}

	// However many distinct key ids the traffic invents, one window buys it one
	// probe — not one fetch per request.
	storm(1)
	require.Equal(t, warm+1, publisher.fetches(),
		"a whole window of invented key ids must cost exactly one probe")

	// The next window buys exactly one more. The guarantee is a rate, so the
	// cost of sustained invented key ids is per interval, not per request.
	clock.advance(jwksProbeInterval)
	storm(2)
	require.Equal(t, warm+2, publisher.fetches(),
		"a second window must buy exactly one more probe")

	// A stopped clock proves the shape of that rate but says nothing about its
	// magnitude: every positive interval suppresses a probe when now never
	// moves. The floor is what makes the rate worth having — at a millisecond
	// spacing an unauthenticated caller could still drive hundreds of fetches a
	// second at the publisher, which is the storm this bound exists to stop.
	require.GreaterOrEqual(t, jwksProbeInterval, time.Second,
		"the probe interval must stay coarse enough to bound the fetch rate")
}

func TestAccessJWKS_ConcurrentColdRequestsShareOneFetch(t *testing.T) {
	pub, priv := mustEd25519(t)
	document := accessJWKSFor(t, pub)

	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(server.Close)

	authz := newJWKSExtAuthz(t, server.URL)
	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requireAdmitted(t, authz, token)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), atomic.LoadInt64(&hits))
}

// ---------------------------------------------------------------------------
// Malformed and hostile key sets
// ---------------------------------------------------------------------------

func TestAccessJWKS_RejectsUnusableKeySets(t *testing.T) {
	pub, _ := mustEd25519(t)
	encodedKey := base64.RawURLEncoding.EncodeToString(pub)
	jwk := fmt.Sprintf(`{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","use":"sig","kid":"k","x":%q}`, encodedKey)

	for name, document := range map[string]string{
		"not json":         `{"keys":`,
		"empty key set":    `{"keys":[]}`,
		"missing key id":   `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","x":"AAAA"}]}`,
		"wrong curve":      `{"keys":[{"kty":"OKP","crv":"X25519","alg":"EdDSA","kid":"k","x":"AAAA"}]}`,
		"wrong algorithm":  `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"RS256","kid":"k","x":"AAAA"}]}`,
		"truncated key":    `{"keys":[{"kty":"OKP","crv":"Ed25519","alg":"EdDSA","kid":"k","x":"AAAA"}]}`,
		"duplicate key id": fmt.Sprintf(`{"keys":[%s,%s]}`, jwk, jwk),
		"too many keys":    manyKeyJWKS(t, jwksMaxKeys+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseJWKS([]byte(document))
			require.Error(t, err)
		})
	}
}

func manyKeyJWKS(t *testing.T, count int) string {
	t.Helper()
	keys := make(map[string]ed25519.PublicKey, count)
	for i := 0; i < count; i++ {
		key, _ := mustEd25519(t)
		keys[fmt.Sprintf("key-%d", i)] = key
	}
	return jwksDocument(keys)
}

func TestAccessJWKS_OversizedResponseIsRefused(t *testing.T) {
	pub, priv := mustEd25519(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("a", jwksMaxBytes+1) + `"}`))
	}))
	t.Cleanup(server.Close)

	authz := newJWKSExtAuthz(t, server.URL)
	requireDenied(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
}

func TestAccessJWKS_RedirectIsNotFollowed(t *testing.T) {
	pub, priv := mustEd25519(t)
	elsewhere := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.server.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	authz := newJWKSExtAuthz(t, redirector.URL)
	requireDenied(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
	require.Zero(t, elsewhere.fetches(), "the configured origin is the only origin")
}

func TestAccessJWKS_SlowPublisherTimesOut(t *testing.T) {
	pub, priv := mustEd25519(t)
	document := accessJWKSFor(t, pub)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(server.Close)

	keys := newAccessJWKS(server.URL)
	keys.cache.timeout = 100 * time.Millisecond
	authz := extAuthzWithKeys(keys)

	started := time.Now()
	requireDenied(t, authz, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
	require.Less(t, time.Since(started), 5*time.Second, "a stalled publisher must not stall the hot path")
}

// ---------------------------------------------------------------------------
// Claim policy is unchanged by kid selection
// ---------------------------------------------------------------------------

func TestAccessJWKS_ClaimPolicyStillApplies(t *testing.T) {
	pub, priv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	keyID := accessKeyID(pub)

	wrongIssuer := validClaims(time.Now())
	wrongIssuer.Issuer = "https://example.com/other-issuer"
	requireDenied(t, authz, signAccessToken(t, priv, keyID, wrongIssuer), 401)

	wrongAudience := validClaims(time.Now())
	wrongAudience.Audience = jwt.ClaimStrings{"someone-else"}
	requireDenied(t, authz, signAccessToken(t, priv, keyID, wrongAudience), 401)

	expired := validClaims(time.Now().Add(-time.Hour))
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-30 * time.Minute))
	requireDenied(t, authz, signAccessToken(t, priv, keyID, expired), 401)

	// A published key id with a signature from a key that is not it.
	_, forger := mustEd25519(t)
	requireDenied(t, authz, signAccessToken(t, forger, keyID, validClaims(time.Now())), 401)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newJWKSExtAuthz(t *testing.T, jwksBaseURL string) *ExtAuthz {
	t.Helper()
	return extAuthzWithKeys(newAccessJWKS(jwksBaseURL))
}

func extAuthzWithKeys(keys accessKeys) *ExtAuthz {
	return &ExtAuthz{
		keys:         keys,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
		revoker:      noopRevoker{},
	}
}

// freezeClock stops a key cache's clock and returns the only thing that moves
// it. A clock that still tracked wall time would let a slow runner cross a
// boundary the test did not ask it to cross. Both verification paths are built
// on the same cache, so both freeze it through here.
func freezeClock[T any](cache *jwksCache[T]) func(time.Duration) {
	var mu sync.Mutex
	at := time.Now()
	cache.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return at
	}
	return func(d time.Duration) {
		mu.Lock()
		at = at.Add(d)
		mu.Unlock()
	}
}

// testClock drives the access-token key cache's TTL, probe, and grace
// boundaries without sleeping.
type testClock struct {
	advanceBy func(time.Duration)
}

func newTestClock(t *testing.T, authz *ExtAuthz) *testClock {
	t.Helper()
	keys, ok := authz.keys.(*accessJWKS)
	require.True(t, ok)
	return &testClock{advanceBy: freezeClock(keys.cache)}
}

func (c *testClock) advance(d time.Duration) { c.advanceBy(d) }

func requireAdmitted(t *testing.T, authz *ExtAuthz, token string) {
	t.Helper()
	resp, err := authz.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "token should have been admitted")
}

func requireDenied(t *testing.T, authz *ExtAuthz, token string, status int32) {
	t.Helper()
	resp, err := authz.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	denied := resp.GetDeniedResponse()
	require.NotNil(t, denied, "token should have been denied")
	require.Equal(t, status, int32(denied.GetStatus().GetCode()))
}

// deniedStatus runs one Check and reports the denial status, for callers on a
// goroutine other than the test's — where require's FailNow is not valid. It
// reports 0 when the request was admitted and -1 when Check itself failed, so
// either shows up as a mismatch once the test goroutine asserts.
func deniedStatus(authz *ExtAuthz, token string) int32 {
	resp, err := authz.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	if err != nil {
		return -1
	}
	denied := resp.GetDeniedResponse()
	if denied == nil {
		return 0
	}
	return int32(denied.GetStatus().GetCode())
}

func readyStatus(t *testing.T, gateway *Gateway) int {
	t.Helper()
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return recorder.Code
}

// TestAccessJWKS_RevocationAppliesToEitherOverlapKey pins the property that
// makes an overlap safe to run: accepting a second verification key widens what
// can be verified, never what is authorized. A revoked jti and a killed session
// are refused whichever half of the overlap set signed the token.
func TestAccessJWKS_RevocationAppliesToEitherOverlapKey(t *testing.T) {
	currentPub, currentPriv := mustEd25519(t)
	retiringPub, retiringPriv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, currentPub, retiringPub))

	revoker := &fakeRevoker{revoked: map[string]bool{}, sessionRevoked: map[string]bool{}}
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	authz.revoker = revoker

	for name, signer := range map[string]ed25519.PrivateKey{
		"current key":  currentPriv,
		"retiring key": retiringPriv,
	} {
		keyID := accessKeyID(currentPub)
		if name == "retiring key" {
			keyID = accessKeyID(retiringPub)
		}
		t.Run(name, func(t *testing.T) {
			claims := validClaims(time.Now())
			token := signAccessToken(t, signer, keyID, claims)
			requireAdmitted(t, authz, token)

			revoker.revoked[claims.ID] = true
			requireDenied(t, authz, token, 401)
			delete(revoker.revoked, claims.ID)

			revoker.sessionRevoked[claims.SessionID] = true
			requireDenied(t, authz, token, 401)
			delete(revoker.sessionRevoked, claims.SessionID)
		})
	}
}

// TestAccessJWKS_OutageDoesNotFetchPerRequest pins the bound that keeps an
// unreachable publisher from costing one round-trip — and one request timeout —
// per arriving token.
func TestAccessJWKS_OutageDoesNotFetchPerRequest(t *testing.T) {
	pub, priv := mustEd25519(t)
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	authz := newJWKSExtAuthz(t, server.URL)
	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))
	for i := 0; i < 25; i++ {
		requireDenied(t, authz, token, 503)
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&hits),
		"a failed fetch must suppress the next one for the backoff window")
}

// TestAccessJWKS_UnknownKeyIDDoesNotBlockARotatedKey is the regression for the
// probe budget that a single unrecognised key id could exhaust. One token
// naming a key nobody publishes used to consume the whole cache window's only
// refetch, so a key rotated in immediately afterwards stayed invisible — and
// every token signed by it was rejected 401 — until the next full refresh.
// Any client could arm that with one unauthenticated request.
func TestAccessJWKS_UnknownKeyIDDoesNotBlockARotatedKey(t *testing.T) {
	k1pub, k1priv := mustEd25519(t)
	k2pub, k2priv := mustEd25519(t)

	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, k1pub))
	authz := newJWKSExtAuthz(t, publisher.server.URL)
	clock := newTestClock(t, authz)

	// Ordinary traffic warms the cache.
	requireAdmitted(t, authz, signAccessToken(t, k1priv, accessKeyID(k1pub), validClaims(time.Now())))

	// One request names a key id nobody publishes. It costs exactly one probe.
	_, attacker := mustEd25519(t)
	requireDenied(t, authz, signAccessToken(t, attacker, "made-up-key-id", validClaims(time.Now())), 401)
	fetchesAfterProbe := publisher.fetches()

	// The rotation lands: K2 is published alongside K1 and starts signing.
	publisher.publish(accessJWKSFor(t, k2pub, k1pub))

	// Within the probe interval the refetch is still rate-limited — that is the
	// bound that keeps untrusted key ids from driving a fetch per request.
	requireDenied(t, authz, signAccessToken(t, k2priv, accessKeyID(k2pub), validClaims(time.Now())), 401)
	require.Equal(t, fetchesAfterProbe, publisher.fetches(),
		"a second unrecognised key id inside the interval must not refetch")

	// Once it passes, the rotated-in key is discovered by the token that names
	// it. The cache has not expired: this is the probe, not a TTL refresh.
	clock.advance(jwksProbeInterval)
	requireAdmitted(t, authz, signAccessToken(t, k2priv, accessKeyID(k2pub), validClaims(time.Now())))
	requireAdmitted(t, authz, signAccessToken(t, k1priv, accessKeyID(k1pub), validClaims(time.Now())))
}
