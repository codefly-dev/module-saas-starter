package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	apigen "api/pkg/gen"
)

// accessClaims mirrors the shape minted by pkg/auth/ed25519.
// Field names use short JSON keys matching the backend minter.
type accessClaims struct {
	jwt.RegisteredClaims
	OrgID          string `json:"org,omitempty"`
	OrgRole        string `json:"or,omitempty"`
	PlatformRole   string `json:"pr,omitempty"`
	SessionID      string `json:"sid"`
	ActingAsUserID string `json:"acting,omitempty"`
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
//	x-acting-as-user-id (only during impersonation)
//
// Provider-specific values (WorkOS sub, WorkOS org id, tokens) NEVER leave
// the sidecar — downstream services see only canonical UUIDs.
type Sidecar struct {
	apiKey    apigen.APIKeyServiceClient
	publicKey ed25519.PublicKey
	issuer    string
	audience  string
}

// NewSidecar constructs a Sidecar. publicKey is the Ed25519 key the backend
// minter uses to sign access tokens; issuer/audience must match the backend
// minter config.
func NewSidecar(backendConn *grpc.ClientConn, publicKey ed25519.PublicKey) *Sidecar {
	return &Sidecar{
		apiKey:    apigen.NewAPIKeyServiceClient(backendConn),
		publicKey: publicKey,
		issuer:    "saas-starter",
		audience:  "saas-starter",
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
	if claims.ActingAsUserID != "" {
		hdrs = append(hdrs, hdr("x-acting-as-user-id", claims.ActingAsUserID))
	}
	return allow(hdrs), nil
}

// checkAPIKey delegates to the backend for api-key validation.
// The backend does the argon2id verify, we just thread the result.
func (s *Sidecar) checkAPIKey(ctx context.Context, key string) (*authv3.CheckResponse, error) {
	resp, err := s.apiKey.ValidateAPIKey(ctx, &apigen.ValidateAPIKeyRequest{KeyHash: key})
	if err != nil {
		log.Printf("api key validate error: %v", err)
		return deny(503, "api key validation failed"), nil
	}
	if !resp.Valid {
		return deny(401, "invalid api key"), nil
	}
	return allow([]*corev3.HeaderValueOption{
		hdr("x-user-id", resp.UserId),
		hdr("x-org-id", resp.OrganizationId),
		hdr("x-scopes", strings.Join(resp.Scopes, ",")),
	}), nil
}

// --- envoy helpers ---

func allow(headers []*corev3.HeaderValueOption) *authv3.CheckResponse {
	return &authv3.CheckResponse{
		Status: &status.Status{Code: int32(codes.OK)},
		HttpResponse: &authv3.CheckResponse_OkResponse{
			OkResponse: &authv3.OkHttpResponse{Headers: headers},
		},
	}
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
