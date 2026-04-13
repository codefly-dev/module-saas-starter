package business

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codefly-dev/core/wool"

	"api/pkg/auth"
	"api/pkg/gen"
)

// AccessTokenLifetime is the TTL baked into minted access tokens.
// Kept in sync with ed25519minter.Config.AccessTokenTTL so the
// ExpiresIn field on Authenticate responses matches reality.
const AccessTokenLifetime = 15 * time.Minute

// Authenticate runs a login or signup through the identity resolver and
// mints a fresh token pair.
//
// Two paths, selected by the request:
//
//  1. **OAuth code flow** (production): profile map carries "code" and
//     "redirect_uri". The code is exchanged at the provider's token
//     endpoint via CodeExchanger, the resulting access_token is
//     validated via TokenValidator, and the resulting claims drive
//     JIT provisioning. Requires both Validator and Exchanger to be
//     wired via the setters.
//
//  2. **Pre-validated path** (dev / tests): provider and provider_id are
//     taken as already-verified by the caller. Used by the dev-admin
//     fixture tests and any in-process flow. No network call to a
//     provider. This path stays available regardless of whether the
//     production validator is wired.
//
// The optional "org_name" in the profile map triggers signup-style
// behaviour: if the user is brand new, an org is created with them as
// owner.
func (s *Service) Authenticate(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	w := wool.Get(ctx).In("Authenticate")

	if s.resolver == nil || s.minter == nil {
		return nil, w.NewError("auth path not wired: resolver/minter missing")
	}

	orgNameOnSignup := ""
	code := ""
	redirectURI := ""
	if req.Profile != nil {
		orgNameOnSignup = req.Profile["org_name"]
		code = req.Profile["code"]
		redirectURI = req.Profile["redirect_uri"]
	}

	var claims *auth.Claims
	var err error
	if code != "" {
		claims, err = s.authenticateWithCode(ctx, req.Provider, code, redirectURI)
		if err != nil {
			return nil, w.Wrapf(err, "oauth code exchange")
		}
	} else {
		claims = &auth.Claims{
			Provider:  req.Provider,
			Subject:   req.ProviderId,
			Email:     req.ProviderEmail,
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}
		if claims.Email == "" {
			claims.Email = req.ProviderId + "@" + req.Provider
		}
	}

	identity, err := s.resolver.Resolve(ctx, claims, orgNameOnSignup)
	if err != nil {
		return nil, w.Wrapf(err, "identity resolution")
	}

	pair, err := s.minter.Mint(ctx, identity)
	if err != nil {
		return nil, w.Wrapf(err, "mint tokens")
	}

	s.emit(ctx, identity.UserID.String(), "user", "auth.login",
		"session", identity.SessionID.String(), identity.OrgID.String())

	return &gen.AuthenticateResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int64(AccessTokenLifetime.Seconds()),
		User:         &gen.User{Uuid: identity.UserID.String()},
	}, nil
}

// authenticateWithCode runs the full OAuth authorization-code flow:
// exchange code → validate access token → extract claims. Requires both
// CodeExchanger and TokenValidator to be wired.
func (s *Service) authenticateWithCode(ctx context.Context, provider, code, redirectURI string) (*auth.Claims, error) {
	if s.exchanger == nil {
		return nil, errors.New("oauth code flow: exchanger not wired")
	}
	if s.validator == nil {
		return nil, errors.New("oauth code flow: validator not wired")
	}
	if redirectURI == "" {
		return nil, errors.New("oauth code flow: redirect_uri required")
	}

	tokens, err := s.exchanger.Exchange(ctx, code, redirectURI)
	if err != nil {
		return nil, err
	}

	// Prefer id_token when present (standard OIDC); fall back to access_token.
	tokenToValidate := tokens.IDToken
	if tokenToValidate == "" {
		tokenToValidate = tokens.AccessToken
	}

	claims, err := s.validator.Validate(ctx, tokenToValidate)
	if err != nil {
		return nil, err
	}
	// Make sure the claims provider matches what the request said — prevents
	// cross-provider smuggling if multiple validators are ever mounted.
	if provider != "" && claims.Provider != "" && provider != claims.Provider {
		return nil, fmt.Errorf("oauth code flow: provider mismatch: request=%q token=%q", provider, claims.Provider)
	}
	return claims, nil
}

// RefreshToken rotates a refresh token, issuing a fresh access + refresh
// pair in the same session family. Reuse of an already-rotated refresh
// kills the entire family via the OWASP rotation pattern.
func (s *Service) RefreshToken(ctx context.Context, req *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
	w := wool.Get(ctx).In("RefreshToken")

	if s.minter == nil {
		return nil, w.NewError("auth path not wired: minter missing")
	}

	pair, err := s.minter.VerifyRefresh(ctx, req.RefreshToken)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRefreshReuse):
			return nil, w.Wrapf(err, "refresh token reuse detected")
		case errors.Is(err, auth.ErrRefreshRevoked):
			return nil, w.Wrapf(err, "invalid refresh token")
		default:
			return nil, w.Wrapf(err, "verify refresh")
		}
	}

	return &gen.RefreshTokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int64(AccessTokenLifetime.Seconds()),
	}, nil
}

// Logout revokes the session family associated with the given refresh
// token. Idempotent: revoking an already-revoked token is a no-op.
func (s *Service) Logout(ctx context.Context, req *gen.LogoutRequest) error {
	w := wool.Get(ctx).In("Logout")

	if s.minter == nil {
		return w.NewError("auth path not wired: minter missing")
	}
	return s.minter.Revoke(ctx, req.RefreshToken)
}

// GetJWKS returns the sidecar-facing JSON Web Key Set.
// Non-authoritative: the sidecar loads its key from Vault directly.
// This endpoint exists for external tooling.
func (s *Service) GetJWKS(_ context.Context) (string, error) {
	if s.minter == nil {
		return `{"keys":[]}`, nil
	}
	return s.minter.JWKS()
}
