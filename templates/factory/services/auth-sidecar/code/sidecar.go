package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	backend "backend/pkg/gen"
)

// isDevMode returns true when running with a dev fixture.
// Usage: codefly run service api --fixture dev-admin
// Enables X-Dev-Role header bypass in the auth sidecar.
func isDevMode() bool {
	fixture := os.Getenv("CODEFLY__FIXTURE")
	return strings.HasPrefix(fixture, "dev")
}

// AccessClaims mirrors the JWT claims from the backend's TokenService.
// Supports both regular sessions and WorkOS impersonation (RFC 8693 `act` claim).
type AccessClaims struct {
	jwt.RegisteredClaims
	OrgID          string    `json:"org,omitempty"`
	OrgRole        string    `json:"org_role,omitempty"`
	PlatformRole   string    `json:"platform_role,omitempty"`
	Act            *ActClaim `json:"act,omitempty"`            // RFC 8693: provider impersonation
	ImpersonatedBy string    `json:"impersonated_by,omitempty"` // local mode impersonation
}

// ActClaim represents the actor in an impersonation session (RFC 8693 Section 4.1).
// When present, `sub` is the impersonated user and `act.sub` is the admin performing impersonation.
// Used by WorkOS, Clerk, Frontegg, and any RFC 8693-compliant provider.
type ActClaim struct {
	Sub string `json:"sub"`
}

// ImpersonationInfo extracts impersonation context from JWT claims.
// Supports two sources:
//  1. RFC 8693 `act` claim (WorkOS, Clerk, standard providers)
//  2. Custom `impersonated_by` claim (our own PlatformAdminService local mode)
type ImpersonationInfo struct {
	IsImpersonated bool
	ImpersonatedBy string // admin user ID
}

func detectImpersonation(claims *AccessClaims) ImpersonationInfo {
	// RFC 8693: act claim (provider-issued impersonation)
	if claims.Act != nil && claims.Act.Sub != "" {
		return ImpersonationInfo{
			IsImpersonated: true,
			ImpersonatedBy: claims.Act.Sub,
		}
	}
	// Custom claim: our PlatformAdminService local impersonation
	if claims.ImpersonatedBy != "" {
		return ImpersonationInfo{
			IsImpersonated: true,
			ImpersonatedBy: claims.ImpersonatedBy,
		}
	}
	return ImpersonationInfo{}
}

// Sidecar implements envoy ext_authz with two auth paths:
//  1. JWT (local Ed25519 validation, no network call)
//  2. API key (cfly_sk_ prefix, validated via backend RPC)
type Sidecar struct {
	apiKey    backend.APIKeyServiceClient
	publicKey ed25519.PublicKey
}

// NewSidecar creates a sidecar with JWT and API key validation.
func NewSidecar(backendConn *grpc.ClientConn, publicKey ed25519.PublicKey) *Sidecar {
	return &Sidecar{
		apiKey:    backend.NewAPIKeyServiceClient(backendConn),
		publicKey: publicKey,
	}
}

// Check implements envoy.service.auth.v3.Authorization.
//
// Auth paths:
//  1. Authorization: Bearer <jwt> → local JWT validation (fast, no network)
//  2. Authorization: Bearer cfly_sk_... → API key validation (backend RPC)
//  3. No auth → pass through (public endpoint)
func (s *Sidecar) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	headers := req.GetAttributes().GetRequest().GetHttp().GetHeaders()

	// Dev mode bypass: X-Dev-Role header skips real auth.
	// Only active when CODEFLY_DEV_MODE=true (set by codefly run in local dev).
	if devRole := headers["x-dev-role"]; devRole != "" && isDevMode() {
		devUser := headers["x-dev-user-id"]
		if devUser == "" {
			devUser = "dev-user-00000000"
		}
		devOrg := headers["x-dev-org-id"]
		if devOrg == "" {
			devOrg = "dev-org-00000000"
		}
		log.Printf("[dev-mode] bypassing auth: role=%s user=%s org=%s", devRole, devUser, devOrg)
		return allow([]*corev3.HeaderValueOption{
			hdr("x-user-id", devUser),
			hdr("x-org-id", devOrg),
			hdr("x-org-role", devRole),
			hdr("x-platform-role", devRole),
			hdr("x-dev-mode", "true"),
		}), nil
	}

	if auth := headers["authorization"]; auth != "" {
		token := strings.TrimPrefix(auth, "Bearer ")
		if token != auth { // had "Bearer " prefix
			if strings.HasPrefix(token, "cfly_sk_") {
				return s.checkAPIKey(ctx, token)
			}
			return s.checkJWT(token)
		}
	}

	// No auth → pass through (public endpoint)
	return allow(nil), nil
}

// checkJWT validates a JWT locally using the Ed25519 public key.
// No network call — just crypto verification.
func (s *Sidecar) checkJWT(tokenString string) (*authv3.CheckResponse, error) {
	if s.publicKey == nil {
		return deny(500, "JWT validation not configured"), nil
	}

	claims := &AccessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	},
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return deny(401, "invalid or expired token"), nil
	}

	hdrs := []*corev3.HeaderValueOption{
		hdr("x-user-id", claims.Subject),
		hdr("x-org-id", claims.OrgID),
		hdr("x-org-role", claims.OrgRole),
		hdr("x-platform-role", claims.PlatformRole),
	}

	// Detect impersonation from either RFC 8693 `act` claim or custom `impersonated_by`
	imp := detectImpersonation(claims)
	if imp.IsImpersonated {
		hdrs = append(hdrs,
			hdr("x-is-impersonated", "true"),
			hdr("x-impersonated-by", imp.ImpersonatedBy),
		)
	} else {
		hdrs = append(hdrs, hdr("x-is-impersonated", "false"))
	}

	return allow(hdrs), nil
}

// checkAPIKey validates an API key by calling the backend.
func (s *Sidecar) checkAPIKey(ctx context.Context, key string) (*authv3.CheckResponse, error) {
	resp, err := s.apiKey.ValidateAPIKey(ctx, &backend.ValidateAPIKeyRequest{
		KeyHash: key,
	})
	if err != nil {
		log.Printf("ERROR validating API key: %v", err)
		return deny(500, "API key validation failed"), nil
	}
	if !resp.Valid {
		return deny(401, "invalid API key"), nil
	}

	return allow([]*corev3.HeaderValueOption{
		hdr("x-user-id", resp.UserId),
		hdr("x-org-id", resp.OrganizationId),
		hdr("x-scopes", strings.Join(resp.Scopes, ",")),
	}), nil
}

// --- helpers ---

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
