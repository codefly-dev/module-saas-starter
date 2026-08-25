package main

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	apigen "auth-sidecar/external/saas-starter/accounts"
)

// accessClaims mirrors the shape minted by pkg/auth/ed25519.
// Field names use short JSON keys matching the backend minter.
type accessClaims struct {
	jwt.RegisteredClaims
	OrgID                 string              `json:"org,omitempty"`
	OrgRole               string              `json:"or,omitempty"`
	PlatformRole          string              `json:"pr,omitempty"`
	ScopedRoles           map[string][]string `json:"sr,omitempty"`
	ScopedRolesTruncated  bool                `json:"srt,omitempty"`
	SessionID             string              `json:"sid"`
	ActingAsUserID        string              `json:"acting,omitempty"`
	MFASatisfied          bool                `json:"mfa,omitempty"`
	AuthenticationMethods []string            `json:"amr,omitempty"`
	AuthenticationTime    *jwt.NumericDate    `json:"auth_time,omitempty"`
	AssuranceLevel        string              `json:"acr,omitempty"`
	MFAVerifiedAt         *jwt.NumericDate    `json:"mfa_at,omitempty"`
	Act                   *actorClaim         `json:"act,omitempty"`
}

// actorClaim is the RFC 8693 `act` on-behalf-of chain (see pkg/auth Actor). The
// sidecar re-emits it verbatim as the x-act header so a downstream service that
// authorizes from forwarded headers still sees who is acting for the user.
type actorClaim struct {
	Subject string      `json:"sub"`
	Act     *actorClaim `json:"act,omitempty"`
}

// Sidecar implements Envoy ext_authz with two auth paths:
//
//  1. Our own Ed25519-signed access token -> local crypto verify, no network.
//  2. API key (cfly_sk_ prefix) -> backend RPC.
//
// Auth requirements (public/required/mfa_pending) are determined by the
// gateway from the route config — the sidecar no longer maintains its own
// public-path allowlist.
//
// On success, the sidecar forwards canonical internal identity headers:
//
//	x-user-id, x-org-id, x-org-role, x-platform-role, x-session-id,
//	x-scoped-roles (JSON scope->roles, only when the caller has scoped grants),
//	x-acting-as-user-id (only during impersonation)
//
// Provider-specific values (WorkOS sub, WorkOS org id, tokens) NEVER leave
// the sidecar — downstream services see only canonical UUIDs.
type Sidecar struct {
	apiKey        apigen.APIKeyServiceClient
	publicKey     ed25519.PublicKey
	issuer        string
	audience      string
	internalToken string
	// previousInternalToken stays accepted alongside internalToken during an
	// overlapping rotation window. Outbound calls always present the current
	// internalToken; only inbound checks honour the previous one.
	previousInternalToken string
	gatewayToken          string
	// revoker enforces access-token revocation on the gateway hot path — the
	// path where accounts trusts the sidecar-stamped identity headers and so
	// never runs its own VerifyAccess revocation check. Always non-nil.
	revoker revoker
	// revocationFailOpen selects the behaviour when the revocation store errors:
	// false (default) denies (fail-closed), true admits (fail-open).
	revocationFailOpen bool
}

// acceptsInternalToken reports whether candidate is the current or a
// still-valid previous internal credential, compared without a timing signal.
// An unset (empty) credential never matches.
func (s *Sidecar) acceptsInternalToken(candidate string) bool {
	if candidate == "" {
		return false
	}
	return constantTimeMatch(candidate, s.internalToken) ||
		constantTimeMatch(candidate, s.previousInternalToken)
}

func constantTimeMatch(candidate, expected string) bool {
	if expected == "" || len(candidate) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(expected)) == 1
}

// NewSidecar constructs a Sidecar. publicKey is the Ed25519 key the backend
// minter uses to sign access tokens; issuer/audience must match the backend
// minter config.
func NewSidecar(backendConn *grpc.ClientConn, publicKey ed25519.PublicKey) *Sidecar {
	return &Sidecar{
		apiKey:                apigen.NewAPIKeyServiceClient(backendConn),
		publicKey:             publicKey,
		issuer:                "saas-starter",
		audience:              "saas-starter",
		internalToken:         workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN"),
		previousInternalToken: workspaceEnv("internal-auth", "CODEFLY_INTERNAL_TOKEN_PREVIOUS"),
		gatewayToken:          workspaceEnv("internal-auth", "CODEFLY_GATEWAY_TOKEN"),
		revoker:               noopRevoker{},
		revocationFailOpen:    revocationFailsOpen(),
	}
}

// SetRevoker wires the access-token revocation list consulted by checkJWT.
// main wires the Redis-backed revoker once the cache connection is resolved;
// left unset the sidecar uses noopRevoker (revocation disabled, dev parity).
func (s *Sidecar) SetRevoker(r revoker) {
	if r != nil {
		s.revoker = r
	}
}

// Check is the Envoy ext_authz hot path.
// The gateway calls this after route matching. The sidecar validates
// credentials and returns identity headers on success.
func (s *Sidecar) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	httpReq := req.GetAttributes().GetRequest().GetHttp()
	headers := httpReq.GetHeaders()

	// Try Bearer token.
	if auth := headers["authorization"]; auth != "" {
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != auth {
			if strings.HasPrefix(token, "cfly_sk_") {
				return s.checkAPIKey(ctx, token)
			}
			return s.checkJWT(ctx, token, httpReq.GetPath())
		}
	}

	// No credentials — deny. The gateway decides whether to enforce
	// or skip based on the route's auth requirement.
	return deny(401, "authentication required"), nil
}

// logoutPaths are the gateway routes that trigger accounts-side token
// revocation (REST and both Connect spellings, from the generated route
// catalogs). checkJWT drops its cached revocation answer for the presented
// jti on these paths — the logout request itself must not shield an
// immediate replay of the token it revokes behind a fresh cache entry.
var logoutPaths = map[string]bool{
	"/v1/auth/logout":                      true,
	"/saas.accounts.v1.AuthService/Logout": true,
	"/customers.AuthService/Logout":        true,
}

// tokenClockSkewLeeway is the exp/nbf tolerance for access-token validation.
// It MUST match accounts' Config.ClockSkew (60s) so a token accepted on the
// direct accounts path is accepted on this gateway path and vice versa — a
// smaller value here would spuriously 401 clock-skewed tokens near expiry.
// accounts writes revocation markers with TTL = remaining-lifetime + this
// leeway (ed25519/minter.go), so a revoked token cannot slip through the
// post-exp acceptance window this leeway opens.
//
// NB: this is a time.Duration; the bare literal 60 would be 60 nanoseconds.
const tokenClockSkewLeeway = 60 * time.Second

// checkJWT runs full alg-locked Ed25519 validation plus iss/aud/exp, consults
// the revocation list, then projects the claims onto forwarded headers.
func (s *Sidecar) checkJWT(ctx context.Context, tokenString, path string) (*authv3.CheckResponse, error) {
	if s.publicKey == nil {
		return deny(503, "JWT validation not configured"), nil
	}

	claims := &accessClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(tokenClockSkewLeeway),
	)
	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != "EdDSA" {
			return nil, fmt.Errorf("alg forbidden: %s", t.Method.Alg())
		}
		return s.publicKey, nil
	})
	if err != nil || !token.Valid {
		return deny(401, "invalid or expired token"), nil
	}

	// Revocation is checked AFTER signature/claim validation so a forged or
	// expired token never reaches the store. This closes the gateway-path gap:
	// accounts only runs its own revocation check on the direct fallback path,
	// trusting the sidecar-stamped identity headers here.
	if claims.ID != "" {
		revoked, err := s.revoker.Revoked(ctx, claims.ID)
		switch {
		case err != nil && !s.revocationFailOpen:
			// Store unreachable in strict mode: deny rather than admit a
			// possibly-revoked token. Recently-seen tokens are still served
			// from the local cache, so this only bites on cache misses during
			// a Redis outage.
			log.Printf("revocation check failed, denying (fail-closed): %v", err)
			return deny(503, "revocation check unavailable"), nil
		case err != nil:
			log.Printf("revocation check failed, admitting (fail-open): %v", err)
		case revoked:
			return deny(401, "token revoked"), nil
		}
		// Forget AFTER the check: the check above may have (re)cached
		// "not revoked" for this jti, and on a logout route that entry
		// would outlive the revocation accounts performs next.
		if pathOnly, _, _ := strings.Cut(path, "?"); logoutPaths[pathOnly] {
			s.revoker.Forget(claims.ID)
		}
	}

	// Session-scoped revocation (admin session-kill). A single marker keyed by
	// the `sid` claim invalidates every access token in the session, covering
	// the path where the admin never held the victim's token. Same fail-closed
	// stance as the jti check.
	if claims.SessionID != "" {
		revoked, err := s.revoker.RevokedSession(ctx, claims.SessionID)
		switch {
		case err != nil && !s.revocationFailOpen:
			log.Printf("session revocation check failed, denying (fail-closed): %v", err)
			return deny(503, "revocation check unavailable"), nil
		case err != nil:
			log.Printf("session revocation check failed, admitting (fail-open): %v", err)
		case revoked:
			return deny(401, "session revoked"), nil
		}
	}

	hdrs := []*corev3.HeaderValueOption{
		hdr("x-user-id", claims.Subject),
		hdr("x-org-id", claims.OrgID),
		hdr("x-org-role", claims.OrgRole),
		hdr("x-platform-role", claims.PlatformRole),
		hdr("x-session-id", claims.SessionID),
	}
	if claims.MFASatisfied {
		hdrs = append(hdrs, hdr("x-mfa-satisfied", "true"))
	}
	if len(claims.AuthenticationMethods) > 0 {
		hdrs = append(hdrs, hdr("x-authentication-methods", strings.Join(claims.AuthenticationMethods, ",")))
	}
	if claims.AuthenticationTime != nil {
		hdrs = append(hdrs, hdr("x-auth-time", strconv.FormatInt(claims.AuthenticationTime.Unix(), 10)))
	}
	if claims.AssuranceLevel != "" {
		hdrs = append(hdrs, hdr("x-assurance-level", claims.AssuranceLevel))
	}
	if claims.MFAVerifiedAt != nil {
		hdrs = append(hdrs, hdr("x-mfa-verified-at", strconv.FormatInt(claims.MFAVerifiedAt.Unix(), 10)))
	}
	if claims.ActingAsUserID != "" {
		hdrs = append(hdrs, hdr("x-acting-as-user-id", claims.ActingAsUserID))
	}
	if claims.Act != nil {
		if encoded, err := json.Marshal(claims.Act); err == nil {
			hdrs = append(hdrs, hdr("x-act", string(encoded)))
		}
	}
	if len(claims.ScopedRoles) > 0 {
		// JSON payload, e.g. {"module-a":["analyst"]}, so downstream services can
		// authorize per-scope roles without calling back into accounts.
		if encoded, err := json.Marshal(claims.ScopedRoles); err == nil {
			hdrs = append(hdrs, hdr("x-scoped-roles", string(encoded)))
		}
	}
	if claims.ScopedRolesTruncated {
		// The grant set exceeded the claim bound; x-scoped-roles is incomplete
		// and a service must fall back to CheckPermission for a full answer.
		hdrs = append(hdrs, hdr("x-scoped-roles-truncated", "true"))
	}
	return s.allow(hdrs), nil
}

// checkAPIKey delegates to the backend for api-key validation.
// We send the plaintext key over TLS; the backend hashes (Vault transit HMAC)
// and verifies, and we just thread the result.
func (s *Sidecar) checkAPIKey(ctx context.Context, key string) (*authv3.CheckResponse, error) {
	if s.gatewayToken == "" {
		return deny(503, "gateway identity signing is not configured"), nil
	}
	if s.internalToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	}
	resp, err := s.apiKey.ValidateAPIKey(ctx, &apigen.ValidateAPIKeyRequest{Key: key})
	if err != nil {
		log.Printf("api key validate error: %v", err)
		return deny(503, "api key validation failed"), nil
	}
	if !resp.Valid {
		return deny(401, "invalid api key"), nil
	}
	return s.allow([]*corev3.HeaderValueOption{
		hdr("x-user-id", resp.UserId),
		hdr("x-org-id", resp.OrganizationId),
		hdr("x-scopes", strings.Join(resp.Scopes, ",")),
	}), nil
}

// --- envoy helpers ---

func (s *Sidecar) allow(headers []*corev3.HeaderValueOption) *authv3.CheckResponse {
	// Return every canonical identity header, including empty values, so Envoy
	// replaces any caller-supplied value even when the authenticated principal
	// has no value for that field.
	values := make(map[string]string, len(headers))
	for _, header := range headers {
		values[strings.ToLower(header.GetHeader().GetKey())] = header.GetHeader().GetValue()
	}
	headers = headers[:0]
	for _, key := range canonicalUpstreamAuthHeaders {
		headers = append(headers, hdr(key, values[key]))
	}
	if s.gatewayToken != "" {
		headers = append(headers, hdr("x-codefly-gateway-token", s.gatewayToken))
	}
	// Every untrusted trust header we did NOT just restamp must be stripped
	// from the upstream request, so a client-spoofed value cannot survive an
	// allow decision. This is the sidecar half of the header-lockstep
	// invariant: the strip set is a superset of the stamped set.
	stamped := make(map[string]struct{}, len(headers))
	for _, header := range headers {
		stamped[strings.ToLower(header.GetHeader().GetKey())] = struct{}{}
	}
	var remove []string
	for _, key := range untrustedAuthHeaders {
		if _, ok := stamped[key]; !ok {
			remove = append(remove, key)
		}
	}
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{
			OkResponse: &authv3.OkHttpResponse{
				Headers:         headers,
				HeadersToRemove: remove,
			},
		},
	}
}

var canonicalUpstreamAuthHeaders = []string{
	"x-user-id", "x-org-id", "x-org-role", "x-platform-role", "x-roles",
	"x-scoped-roles", "x-scoped-roles-truncated", "x-auth-id", "x-user-email", "x-user-name", "x-session-id",
	"x-acting-as-user-id", "x-act", "x-scopes", "x-mfa-satisfied",
	"x-authentication-methods", "x-auth-time", "x-assurance-level", "x-mfa-verified-at",
}

func deny(httpCode int, body string) *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.PermissionDenied)},
		HttpResponse: &authv3.CheckResponse_DeniedResponse{
			DeniedResponse: &authv3.DeniedHttpResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode(httpCode)},
				Body:   body,
			},
		},
	}
}

func hdr(key, value string) *corev3.HeaderValueOption {
	return &corev3.HeaderValueOption{
		Header: &corev3.HeaderValue{Key: key, Value: value},
	}
}
