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

func (f fixedAccessKeys) usable() bool { return len(f) > 0 }

// unavailableAccessKeys stands in for a gateway that has not managed to fetch
// the published key set at all.
type unavailableAccessKeys struct{}

func (unavailableAccessKeys) keyFor(context.Context, string) (ed25519.PublicKey, error) {
	return nil, fmt.Errorf("%w: test", errNoVerificationKeys)
}

func (unavailableAccessKeys) usable() bool { return false }

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
	sidecar := newJWKSSidecar(t, publisher.server.URL)

	requireAdmitted(t, sidecar, signAccessToken(t, firstPriv, accessKeyID(firstPub), validClaims(time.Now())))

	// accounts publishes the incoming key alongside the outgoing one and starts
	// signing with it. The gateway is not restarted.
	publisher.publish(accessJWKSFor(t, secondPub, firstPub))

	requireAdmitted(t, sidecar, signAccessToken(t, secondPriv, accessKeyID(secondPub), validClaims(time.Now())))
	requireAdmitted(t, sidecar, signAccessToken(t, firstPriv, accessKeyID(firstPub), validClaims(time.Now())))
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
			sidecar := newJWKSSidecar(t, publisher.server.URL)

			requireAdmitted(t, sidecar, signAccessToken(t, currentPriv, accessKeyID(currentPub), validClaims(time.Now())))
			requireAdmitted(t, sidecar, signAccessToken(t, retiringPriv, accessKeyID(retiringPub), validClaims(time.Now())))
		})
	}
}

func TestAccessJWKS_RetiredKeyIsRejectedAfterConvergence(t *testing.T) {
	currentPub, _ := mustEd25519(t)
	retiredPub, retiredPriv := mustEd25519(t)

	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, currentPub, retiredPub))
	sidecar := newJWKSSidecar(t, publisher.server.URL)
	clock := newTestClock(t, sidecar)

	retiredToken := signAccessToken(t, retiredPriv, accessKeyID(retiredPub), validClaims(time.Now()))
	requireAdmitted(t, sidecar, retiredToken)

	// accounts drops the retired key. The gateway converges within one cache
	// TTL; the stale grace only applies while the publisher is unreachable.
	publisher.publish(accessJWKSFor(t, currentPub))
	clock.advance(accessJWKSCacheTTL + time.Second)

	requireDenied(t, sidecar, retiredToken, 401)
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

	sidecar := newJWKSSidecar(t, server.URL)
	clock := newTestClock(t, sidecar)
	gateway := NewGateway(sidecar, NewRouteMatcher(testRouteEntries(), nil),
		map[string]*url.URL{"accounts": MustURL(server.URL), "frontend": MustURL(server.URL)}, nil)

	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))
	requireDenied(t, sidecar, token, 503)
	require.False(t, sidecar.canVerifyAccessTokens())
	require.Equal(t, http.StatusServiceUnavailable, readyStatus(t, gateway))

	// accounts finishes starting. No gateway restart, no operator action.
	reachable.Store(true)
	clock.advance(jwksFailureBackoff)

	requireAdmitted(t, sidecar, token)
	require.True(t, sidecar.canVerifyAccessTokens())
	require.Equal(t, http.StatusOK, readyStatus(t, gateway))
}

func TestAccessJWKS_CachedKeysSurviveAPublisherOutage(t *testing.T) {
	pub, priv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	sidecar := newJWKSSidecar(t, publisher.server.URL)
	clock := newTestClock(t, sidecar)

	requireAdmitted(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	publisher.server.Close()

	// Within the documented grace the cached key set still verifies, so an
	// accounts restart does not take authentication down with it.
	clock.advance(accessJWKSCacheTTL + time.Minute)
	requireAdmitted(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())))
	require.True(t, sidecar.canVerifyAccessTokens())

	// Past it the gateway fails closed: an unreachable publisher must not keep
	// a withdrawn key alive indefinitely.
	clock.advance(accessJWKSStaleGrace)
	requireDenied(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
	require.False(t, sidecar.canVerifyAccessTokens())
}

// ---------------------------------------------------------------------------
// Bounded fetching under untrusted input
// ---------------------------------------------------------------------------

func TestAccessJWKS_ConcurrentUnknownKeyIDsAreBounded(t *testing.T) {
	pub, _ := mustEd25519(t)
	_, attacker := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	sidecar := newJWKSSidecar(t, publisher.server.URL)

	const callers = 32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token := signAccessToken(t, attacker, fmt.Sprintf("attacker-chosen-%d", i), validClaims(time.Now()))
			requireDenied(t, sidecar, token, 401)
		}(i)
	}
	wg.Wait()

	// One cold fetch plus at most one unknown-key probe for the whole window,
	// however many distinct key ids the traffic invents.
	require.LessOrEqual(t, publisher.fetches(), int64(2))
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

	sidecar := newJWKSSidecar(t, server.URL)
	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requireAdmitted(t, sidecar, token)
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

	sidecar := newJWKSSidecar(t, server.URL)
	requireDenied(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
}

func TestAccessJWKS_RedirectIsNotFollowed(t *testing.T) {
	pub, priv := mustEd25519(t)
	elsewhere := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.server.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	sidecar := newJWKSSidecar(t, redirector.URL)
	requireDenied(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
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
	sidecar := sidecarWithKeys(keys)

	started := time.Now()
	requireDenied(t, sidecar, signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now())), 503)
	require.Less(t, time.Since(started), 5*time.Second, "a stalled publisher must not stall the hot path")
}

// ---------------------------------------------------------------------------
// Claim policy is unchanged by kid selection
// ---------------------------------------------------------------------------

func TestAccessJWKS_ClaimPolicyStillApplies(t *testing.T) {
	pub, priv := mustEd25519(t)
	publisher := newRotatingJWKSServer(t, accessJWKSFor(t, pub))
	sidecar := newJWKSSidecar(t, publisher.server.URL)
	keyID := accessKeyID(pub)

	wrongIssuer := validClaims(time.Now())
	wrongIssuer.Issuer = "https://example.com/other-issuer"
	requireDenied(t, sidecar, signAccessToken(t, priv, keyID, wrongIssuer), 401)

	wrongAudience := validClaims(time.Now())
	wrongAudience.Audience = jwt.ClaimStrings{"someone-else"}
	requireDenied(t, sidecar, signAccessToken(t, priv, keyID, wrongAudience), 401)

	expired := validClaims(time.Now().Add(-time.Hour))
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-30 * time.Minute))
	requireDenied(t, sidecar, signAccessToken(t, priv, keyID, expired), 401)

	// A published key id with a signature from a key that is not it.
	_, forger := mustEd25519(t)
	requireDenied(t, sidecar, signAccessToken(t, forger, keyID, validClaims(time.Now())), 401)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newJWKSSidecar(t *testing.T, jwksBaseURL string) *Sidecar {
	t.Helper()
	return sidecarWithKeys(newAccessJWKS(jwksBaseURL))
}

func sidecarWithKeys(keys accessKeys) *Sidecar {
	return &Sidecar{
		keys:         keys,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
		revoker:      noopRevoker{},
	}
}

// testClock replaces the key cache's clock so TTL and grace boundaries are
// exercised without sleeping.
type testClock struct {
	mu     sync.Mutex
	offset time.Duration
	keys   *accessJWKS
}

func newTestClock(t *testing.T, sidecar *Sidecar) *testClock {
	t.Helper()
	keys, ok := sidecar.keys.(*accessJWKS)
	require.True(t, ok)
	clock := &testClock{keys: keys}
	keys.cache.now = func() time.Time {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		return time.Now().Add(clock.offset)
	}
	return clock
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.offset += d
	c.mu.Unlock()
}

func requireAdmitted(t *testing.T, sidecar *Sidecar, token string) {
	t.Helper()
	resp, err := sidecar.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse(), "token should have been admitted")
}

func requireDenied(t *testing.T, sidecar *Sidecar, token string, status int32) {
	t.Helper()
	resp, err := sidecar.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	denied := resp.GetDeniedResponse()
	require.NotNil(t, denied, "token should have been denied")
	require.Equal(t, status, int32(denied.GetStatus().GetCode()))
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
	sidecar := newJWKSSidecar(t, publisher.server.URL)
	sidecar.revoker = revoker

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
			requireAdmitted(t, sidecar, token)

			revoker.revoked[claims.ID] = true
			requireDenied(t, sidecar, token, 401)
			delete(revoker.revoked, claims.ID)

			revoker.sessionRevoked[claims.SessionID] = true
			requireDenied(t, sidecar, token, 401)
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

	sidecar := newJWKSSidecar(t, server.URL)
	token := signAccessToken(t, priv, accessKeyID(pub), validClaims(time.Now()))
	for i := 0; i < 25; i++ {
		requireDenied(t, sidecar, token, 503)
	}
	require.Equal(t, int64(1), atomic.LoadInt64(&hits),
		"a failed fetch must suppress the next one for the backoff window")
}
