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
	}
}

// Check is the Envoy ext_authz hot path.
// The gateway calls this after route matching. The sidecar validates
// credentials and returns identity headers on success.
func (s *Sidecar) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	headers := req.GetAttributes().GetRequest().GetHttp().GetHeaders()

	// Try Bearer token.
	if auth := headers["authorization"]; auth != "" {
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != auth {
			if strings.HasPrefix(token, "cfly_sk_") {
				return s.checkAPIKey(ctx, token)
			}
			return s.checkJWT(token)
		}
	}

	// No credentials — deny. The gateway decides whether to enforce
	// or skip based on the route's auth requirement.
	return deny(401, "authentication required"), nil
}

// checkJWT runs full alg-locked Ed25519 validation plus iss/aud/exp, then
// projects the claims onto forwarded headers.
func (s *Sidecar) checkJWT(tokenString string) (*authv3.CheckResponse, error) {
	if s.publicKey == nil {
		return deny(503, "JWT validation not configured"), nil
	}

	claims := &accessClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(60),
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
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{
			OkResponse: &authv3.OkHttpResponse{Headers: headers},
		},
	}
}

var canonicalUpstreamAuthHeaders = []string{
	"x-user-id", "x-org-id", "x-org-role", "x-platform-role", "x-roles",
	"x-scoped-roles", "x-scoped-roles-truncated", "x-auth-id", "x-user-email", "x-user-name", "x-session-id",
	"x-acting-as-user-id", "x-scopes", "x-mfa-satisfied",
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
