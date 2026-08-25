package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// These tests exercise the sidecar in isolation. They don't require the
// full codefly stack — the sidecar is constructed with a generated Ed25519
// key, and the api-key path is stubbed out (nil client — api-key requests
// panic, which is fine: we don't exercise them here).

func newTestSidecar(t *testing.T) (*Sidecar, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &Sidecar{
		publicKey:    pub,
		issuer:       "saas-starter",
		audience:     "saas-starter",
		gatewayToken: "test-gateway-token",
		revoker:      noopRevoker{},
	}, priv
}

// signClaims produces an EdDSA-signed JWT with the given claims.
func signClaims(t *testing.T, priv ed25519.PrivateKey, c accessClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func validClaims(now time.Time) accessClaims {
	sessionID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	return accessClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "saas-starter",
			Subject:   userID.String(),
			Audience:  jwt.ClaimStrings{"saas-starter"},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			ID:        "jti-" + uuid.Must(uuid.NewV7()).String(),
		},
		OrgID:        orgID.String(),
		OrgRole:      "admin",
		PlatformRole: "super_admin",
		SessionID:    sessionID.String(),
	}
}

func headerMap(resp *authv3.CheckResponse) map[string]string {
	m := map[string]string{}
	ok := resp.GetOkResponse()
	if ok == nil {
		return m
	}
	for _, h := range ok.GetHeaders() {
		m[h.GetHeader().GetKey()] = h.GetHeader().GetValue()
	}
	return m
}

func checkReq(path string, headers map[string]string) *authv3.CheckRequest {
	return &authv3.CheckRequest{
		Attributes: &authv3.AttributeContext{
			Request: &authv3.AttributeContext_Request{
				Http: &authv3.AttributeContext_HttpRequest{
					Headers: headers,
					Path:    path,
				},
			},
		},
	}
}

// ============================================================================
// No credentials — sidecar denies (gateway decides whether to enforce)
// ============================================================================

func TestUnit_NoCredentials_Denied(t *testing.T) {
	s, _ := newTestSidecar(t)
	ctx := context.Background()

	// The sidecar no longer has a public-path allowlist. It always
	// denies when no credentials are provided. The gateway is
	// responsible for checking the route's auth requirement and
	// deciding whether to forward or reject.
	paths := []string{
		"/v1/auth/authenticate",
		"/v1/users",
		"/health",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resp, err := s.Check(ctx, checkReq(path, map[string]string{}))
			require.NoError(t, err)
			require.NotNil(t, resp.GetDeniedResponse(),
				"sidecar must deny when no credentials are provided")
		})
	}
}

// ============================================================================
// JWT path
// ============================================================================

func TestUnit_ValidJWT_ForwardsHeaders(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now())
	token := signClaims(t, priv, c)

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())

	h := headerMap(resp)
	require.Equal(t, c.Subject, h["x-user-id"])
	require.Equal(t, c.OrgID, h["x-org-id"])
	require.Equal(t, c.OrgRole, h["x-org-role"])
	require.Equal(t, c.PlatformRole, h["x-platform-role"])
	require.Equal(t, c.SessionID, h["x-session-id"])
	require.Equal(t, "test-gateway-token", h["x-codefly-gateway-token"])
	require.Empty(t, h["x-acting-as-user-id"], "acting header is empty unless impersonating")
}

// TestUnit_Allow_RemovesUnstampedTrustHeaders locks the H4 fix: on an allow
// decision the sidecar must instruct Envoy to strip every untrusted trust
// header it does not restamp, so a client-spoofed header cannot survive to the
// upstream. This is the sidecar half of the header-lockstep invariant paired
// with M6 (the strip set is a superset of the stamped set).
func TestUnit_Allow_RemovesUnstampedTrustHeaders(t *testing.T) {
	s, priv := newTestSidecar(t)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + signClaims(t, priv, validClaims(time.Now())),
	}))
	require.NoError(t, err)
	ok := resp.GetOkResponse()
	require.NotNil(t, ok)

	stamped := map[string]struct{}{}
	for _, h := range ok.GetHeaders() {
		stamped[strings.ToLower(h.GetHeader().GetKey())] = struct{}{}
	}
	removed := map[string]struct{}{}
	for _, k := range ok.GetHeadersToRemove() {
		removed[strings.ToLower(k)] = struct{}{}
	}

	// Lockstep: every untrusted trust header is either restamped or removed,
	// and never both.
	for _, k := range untrustedAuthHeaders {
		_, isStamped := stamped[k]
		_, isRemoved := removed[k]
		require.Truef(t, isStamped || isRemoved, "untrusted header %q is neither restamped nor removed", k)
		require.Falsef(t, isStamped && isRemoved, "untrusted header %q is both restamped and removed", k)
	}

	// Trust headers the sidecar never stamps on an allow must be removed.
	require.Contains(t, removed, "x-codefly-internal-token")
	require.Contains(t, removed, "x-codefly-public-origin")

	// A restamped header is overwritten, not removed.
	require.NotContains(t, removed, "x-user-id")
	require.NotContains(t, removed, "x-codefly-gateway-token")
}

func TestUnit_ValidJWT_ForwardsActorChainAndOverwritesSpoofedHeader(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.Act = &actorClaim{Subject: "svc:billing-worker", Act: &actorClaim{Subject: "svc:gateway"}}
	token := signClaims(t, priv, c)

	// The caller supplies a forged x-act; the sidecar must emit the verified
	// chain, replacing it.
	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
		"x-act":         `{"sub":"svc:attacker"}`,
	}))
	require.NoError(t, err)
	require.Equal(t, `{"sub":"svc:billing-worker","act":{"sub":"svc:gateway"}}`, headerMap(resp)["x-act"])
}

func TestUnit_ValidJWT_EmitsEmptyActorHeaderForDirectSession(t *testing.T) {
	s, priv := newTestSidecar(t)
	token := signClaims(t, priv, validClaims(time.Now()))

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
		"x-act":         `{"sub":"svc:attacker"}`,
	}))
	require.NoError(t, err)
	h := headerMap(resp)
	require.Contains(t, h, "x-act", "canonical header is always emitted")
	require.Empty(t, h["x-act"], "no chain → empty value overwrites any spoofed x-act")
}

func TestUnit_ValidJWT_ForwardsScopedRoles(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.ScopedRoles = map[string][]string{"module-a": {"analyst"}, "module-b": {"admin"}}
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())

	h := headerMap(resp)
	var got map[string][]string
	require.NoError(t, json.Unmarshal([]byte(h["x-scoped-roles"]), &got))
	require.Equal(t, c.ScopedRoles, got)
}

func TestUnit_ValidJWT_ForwardsScopedRolesTruncated(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.ScopedRoles = map[string][]string{"module-a": {"analyst"}}
	c.ScopedRolesTruncated = true
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)

	h := headerMap(resp)
	require.Equal(t, "true", h["x-scoped-roles-truncated"])

	// Absent (empty) when the grant set fit within the bound.
	c.ScopedRolesTruncated = false
	resp, err = s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + signClaims(t, priv, c),
	}))
	require.NoError(t, err)
	require.Empty(t, headerMap(resp)["x-scoped-roles-truncated"])
}

func TestUnit_ValidJWT_NoScopedRolesOmitsHeader(t *testing.T) {
	s, priv := newTestSidecar(t)
	token := signClaims(t, priv, validClaims(time.Now()))

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)

	// The canonical-header sweep still emits the key so a spoofed value is
	// overwritten, but with an empty value when the caller has no scoped roles.
	require.Empty(t, headerMap(resp)["x-scoped-roles"])
}

func TestUnit_ValidJWT_ForwardsMFAState(t *testing.T) {
	s, priv := newTestSidecar(t)
	c := validClaims(time.Now())
	c.MFASatisfied = true
	c.AuthenticationMethods = []string{"oauth", "otp"}
	c.AuthenticationTime = jwt.NewNumericDate(time.Now().Add(-2 * time.Minute).Truncate(time.Second))
	c.AssuranceLevel = "aal2"
	c.MFAVerifiedAt = jwt.NewNumericDate(time.Now().Add(-time.Minute).Truncate(time.Second))
	token := signClaims(t, priv, c)

	resp, err := s.Check(context.Background(), checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	headers := headerMap(resp)
	require.Equal(t, "true", headers["x-mfa-satisfied"])
	require.Equal(t, "oauth,otp", headers["x-authentication-methods"])
	require.Equal(t, strconv.FormatInt(c.AuthenticationTime.Unix(), 10), headers["x-auth-time"])
	require.Equal(t, "aal2", headers["x-assurance-level"])
	require.Equal(t, strconv.FormatInt(c.MFAVerifiedAt.Unix(), 10), headers["x-mfa-verified-at"])
}

func TestUnit_ValidJWT_Impersonation_ForwardsActingHeader(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now())
	target := uuid.Must(uuid.NewV7())
	c.ActingAsUserID = target.String()

	token := signClaims(t, priv, c)

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetOkResponse())

	h := headerMap(resp)
	require.Equal(t, c.Subject, h["x-user-id"], "x-user-id stays as the actor for audit")
	require.Equal(t, target.String(), h["x-acting-as-user-id"], "acting header carries the target")
}

func TestUnit_ExpiredJWT_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now().Add(-20 * time.Minute))
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-5 * time.Minute))
	token := signClaims(t, priv, c)

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}

func TestUnit_WrongIssuer_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now())
	c.Issuer = "some-other-issuer"
	token := signClaims(t, priv, c)

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}

func TestUnit_WrongAudience_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now())
	c.Audience = jwt.ClaimStrings{"different-audience"}
	token := signClaims(t, priv, c)

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + token,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}

func TestUnit_ForgedSignature_Denied(t *testing.T) {
	s, priv := newTestSidecar(t)
	ctx := context.Background()

	c := validClaims(time.Now())
	token := signClaims(t, priv, c)

	// Flip a bit in the signature segment.
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 0xFF
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	tampered := strings.Join(parts, ".")

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + tampered,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}

func TestUnit_AlgNone_Denied(t *testing.T) {
	s, _ := newTestSidecar(t)
	ctx := context.Background()

	// Manually build an alg:none token — no signature.
	header := `{"alg":"none","typ":"JWT"}`
	payload := `{"iss":"saas-starter","aud":"saas-starter","sub":"` +
		uuid.Must(uuid.NewV7()).String() + `","exp":9999999999,"sid":"` +
		uuid.Must(uuid.NewV7()).String() + `"}`
	enc := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	forged := enc(header) + "." + enc(payload) + "."

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer " + forged,
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse(), "alg:none must be rejected")
}

func TestUnit_MalformedJWT_Denied(t *testing.T) {
	s, _ := newTestSidecar(t)
	ctx := context.Background()

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer not.even.jwt",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
}

func TestUnit_NoKey_Denied(t *testing.T) {
	// Simulates a sidecar that failed to fetch the JWKS.
	s := &Sidecar{publicKey: nil, issuer: "saas-starter", audience: "saas-starter"}
	ctx := context.Background()

	resp, err := s.Check(ctx, checkReq("/v1/users", map[string]string{
		"authorization": "Bearer anything",
	}))
	require.NoError(t, err)
	require.NotNil(t, resp.GetDeniedResponse())
	require.Equal(t, int32(503), int32(resp.GetDeniedResponse().Status.Code))
}

// Suppress unused import warnings on narrow builds.
var _ = corev3.HeaderValueOption{}
