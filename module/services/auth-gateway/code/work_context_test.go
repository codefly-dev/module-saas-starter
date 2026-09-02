package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

func mustEd25519(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return pub, priv
}

func jwksDocument(keys map[string]ed25519.PublicKey) string {
	document := struct {
		Keys []map[string]string `json:"keys"`
	}{}
	for kid, pub := range keys {
		document.Keys = append(document.Keys, map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig",
			"kid": kid, "x": base64.RawURLEncoding.EncodeToString(pub),
		})
	}
	raw, _ := json.Marshal(document)
	return string(raw)
}

// jwksServer serves the given JWKS document and counts how many times it was
// fetched, so tests can assert that unknown key ids do not stampede refetches.
func jwksServer(t *testing.T, document string, hits *int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits != nil {
			atomic.AddInt64(hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(document))
	}))
	t.Cleanup(server.Close)
	return server
}

func mintWorkContext(t *testing.T, kid string, priv ed25519.PrivateKey, now func() time.Time) codefly.WorkContextToken {
	t.Helper()
	signer, err := codefly.NewWorkContextSigner(codefly.WorkContextSignerOptions{
		Issuer:     "saas-starter",
		KeyID:      kid,
		PrivateKey: priv,
		Now:        now,
	})
	require.NoError(t, err)
	token, _, err := signer.StartTask(codefly.StartTaskInput{
		Audience:         "solution:demo",
		TenantID:         "tenant-1",
		OwnerPrincipalID: "user-1",
		TaskID:           "task-1",
		SessionID:        "session-1",
		AuthorityScopes: []*basev0.WorkScopeV1{
			{ResourceKind: "audit", Actions: []string{"read"}},
		},
	})
	require.NoError(t, err)
	return token
}

func TestWorkContextVerifier_ValidTokenPasses(t *testing.T) {
	pub, priv := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)

	verifier := newWorkContextVerifier(server.URL)
	err := verifier.Verify(context.Background(), mintWorkContext(t, kid, priv, nil))
	require.NoError(t, err)
}

func TestWorkContextVerifier_ForgedSignatureFailsClosed(t *testing.T) {
	pub, _ := mustEd25519(t)
	_, attacker := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)

	verifier := newWorkContextVerifier(server.URL)
	// Claims the published key id but is signed by a key the JWKS never lists.
	err := verifier.Verify(context.Background(), mintWorkContext(t, kid, attacker, nil))
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)
}

func TestWorkContextVerifier_ExpiredTokenFailsClosed(t *testing.T) {
	pub, priv := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)

	past := func() time.Time { return time.Now().Add(-time.Hour) }
	verifier := newWorkContextVerifier(server.URL)
	err := verifier.Verify(context.Background(), mintWorkContext(t, kid, priv, past))
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)
}

func TestWorkContextVerifier_JWKSUnreachableFailsClosed(t *testing.T) {
	_, priv := mustEd25519(t)
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{"key-1": priv.Public().(ed25519.PublicKey)}), nil)
	url := server.URL
	server.Close()

	verifier := newWorkContextVerifier(url)
	err := verifier.Verify(context.Background(), mintWorkContext(t, "key-1", priv, nil))
	// An upstream outage must read as the same invalid sentinel, not a distinct
	// transport error class that would leak the dependency being down.
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)
}

func TestWorkContextVerifier_UnknownKeyIDDoesNotStampede(t *testing.T) {
	pub, _ := mustEd25519(t)
	_, other := mustEd25519(t)
	var hits int64
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{"key-1": pub}), &hits)

	verifier := newWorkContextVerifier(server.URL)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		err := verifier.Verify(ctx, mintWorkContext(t, "rotated-away", other, nil))
		require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)
	}
	// One warm fetch plus at most one unknown-key probe within the cache window.
	require.LessOrEqual(t, atomic.LoadInt64(&hits), int64(2))
}

func TestWorkContextVerifier_PicksUpRotatedKey(t *testing.T) {
	current, currentPriv := mustEd25519(t)
	next, nextPriv := mustEd25519(t)

	var document atomic.Pointer[string]
	only := jwksDocument(map[string]ed25519.PublicKey{"key-1": current})
	document.Store(&only)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*document.Load()))
	}))
	t.Cleanup(server.Close)

	verifier := newWorkContextVerifier(server.URL)
	ctx := context.Background()
	require.NoError(t, verifier.Verify(ctx, mintWorkContext(t, "key-1", currentPriv, nil)))

	// Rotate: publish key-2 alongside key-1. The cache is still fresh, so the
	// unknown-key probe — not a TTL refresh — must discover the new key.
	rotated := jwksDocument(map[string]ed25519.PublicKey{"key-1": current, "key-2": next})
	document.Store(&rotated)
	require.NoError(t, verifier.Verify(ctx, mintWorkContext(t, "key-2", nextPriv, nil)))
}

func TestWorkContextVerifier_RefreshWarmsKeys(t *testing.T) {
	pub, priv := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	url := server.URL

	verifier := newWorkContextVerifier(url)
	require.NoError(t, verifier.Refresh(context.Background()))

	// Keys are warm, so a valid token verifies from cache even once the
	// publisher is unreachable.
	server.Close()
	err := verifier.Verify(context.Background(), mintWorkContext(t, kid, priv, nil))
	require.NoError(t, err)
}

func TestWorkContextVerifier_RefreshFailsClosedWhenUnreachable(t *testing.T) {
	server := jwksServer(t, `{"keys":[]}`, nil)
	url := server.URL
	server.Close()

	verifier := newWorkContextVerifier(url)
	require.ErrorIs(t, verifier.Refresh(context.Background()), codefly.ErrWorkContextInvalid)
}

func TestParseWorkContextJWKS_RejectsEmptyKeySet(t *testing.T) {
	_, err := parseWorkContextJWKS([]byte(`{"keys":[]}`))
	require.ErrorIs(t, err, codefly.ErrWorkContextInvalid)
}

func TestParseWorkContextJWKS_SkipsNonEd25519Keys(t *testing.T) {
	pub, _ := mustEd25519(t)
	document := fmt.Sprintf(
		`{"keys":[{"kty":"RSA","kid":"rsa-1"},{"kty":"OKP","crv":"Ed25519","use":"sig","kid":"ed-1","x":%q}]}`,
		base64.RawURLEncoding.EncodeToString(pub),
	)
	keys, err := parseWorkContextJWKS([]byte(document))
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Contains(t, keys, "ed-1")
}

func TestGateway_InvalidWorkContextReturns401(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)
	pub, _ := mustEd25519(t)
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{"key-1": pub}), nil)
	gw.workContext = newWorkContextVerifier(server.URL)

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set(codefly.WorkContextHeaderName, "not-a-valid-work-context")
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Nil(t, apiFake.lastHeaders, "invalid Work Context must not reach the upstream")
}

func TestGateway_ValidWorkContextForwards(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)
	pub, wcPriv := mustEd25519(t)
	const kid = "key-1"
	server := jwksServer(t, jwksDocument(map[string]ed25519.PublicKey{kid: pub}), nil)
	gw.workContext = newWorkContextVerifier(server.URL)

	token := mintWorkContext(t, kid, wcPriv, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	req.Header.Set(codefly.WorkContextHeaderName, token.Encoded())
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, apiFake.lastHeaders)
	require.Equal(t, token.Encoded(), apiFake.lastHeaders.Get(codefly.WorkContextHeaderName),
		"a verified Work Context is forwarded unchanged to the callee")
}

func TestGateway_NoWorkContextHeaderUnaffected(t *testing.T) {
	gw, apiFake, _, priv := newGatewayHarness(t)
	// Configured verifier, but no header presented: request must route normally.
	gw.workContext = newWorkContextVerifier("http://127.0.0.1:0")

	req := httptest.NewRequest(http.MethodGet, "/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+signValidToken(t, priv))
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, apiFake.lastHeaders)
}
