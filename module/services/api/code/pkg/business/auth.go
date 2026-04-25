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
	state := ""
	codeVerifier := ""
	if req.Profile != nil {
		orgNameOnSignup = req.Profile["org_name"]
		code = req.Profile["code"]
		redirectURI = req.Profile["redirect_uri"]
		state = req.Profile["state"]
		codeVerifier = req.Profile["code_verifier"]
	}

	var claims *auth.Claims
	var err error
	if code != "" {
		// Server-side state validation (defense in depth on top of the
		// FE's sessionStorage check). When the signer is wired, the
		// state parameter MUST be present and valid; without the signer
		// we accept the legacy flow for back-compat with existing FEs.
		if s.oauthState != nil {
			if state == "" {
				return nil, w.NewError("oauth code flow: state required")
			}
			if err := s.oauthState.Verify(state, req.Provider, redirectURI); err != nil {
				// Surface as the canonical sentinel — never leak the
				// specific cause (sig vs exp vs binding) to the client.
				return nil, w.Wrapf(auth.ErrInvalidOAuthState, "oauth state")
			}
		}
		claims, err = s.authenticateWithCode(ctx, req.Provider, code, redirectURI, codeVerifier)
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

	// MFA gate: users with no enrolled device have nothing to challenge,
	// so the session counts as "MFA satisfied" automatically. Users with
	// a verified device need to clear a TOTP / backup-code challenge —
	// for the dev-only pre-validated path we currently treat them the
	// same (a follow-up will add VerifyMFAChallenge between Authenticate
	// and the FE storing tokens). The token still carries the flag so
	// downstream requireMFA gates work uniformly.
	enrolled, mErr := s.store.HasVerifiedMFA(ctx, identity.UserID.String())
	if mErr != nil {
		w.Warn("HasVerifiedMFA lookup failed; defaulting to mfa_satisfied=false",
			wool.ErrField(mErr))
	}
	identity.MFASatisfied = !enrolled

	pair, err := s.minter.Mint(ctx, identity)
	if err != nil {
		return nil, w.Wrapf(err, "mint tokens")
	}

	s.emit(ctx, identity.UserID.String(), "user", "auth.login",
		"session", identity.SessionID.String(), identity.OrgID.String())

	// Return the full user record so the caller (frontend, test, CLI) has
	// PrimaryEmail, Status, Profile etc. in one response — previously only
	// Uuid was populated, which forced every caller to make a follow-up
	// GetUser call and broke the login-flow integration tests.
	user, err := s.store.GetUser(ctx, identity.UserID.String())
	if err != nil || user == nil {
		// Not fatal: the tokens are valid even if user hydration fails.
		// Fall back to a skeleton User so existing clients keep working.
		user = &gen.User{Uuid: identity.UserID.String()}
	}

	return &gen.AuthenticateResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    int64(AccessTokenLifetime.Seconds()),
		User:         user,
	}, nil
}

// authenticateWithCode runs the full OAuth authorization-code flow:
// exchange code → validate access token → extract claims. Requires both
// CodeExchanger and TokenValidator to be wired.
//
// codeVerifier is the FE-generated PKCE secret (empty for legacy
// clients). Forwarded to the provider's token endpoint so PKCE binds
// the code redemption to the original authorize request.
func (s *Service) authenticateWithCode(ctx context.Context, provider, code, redirectURI, codeVerifier string) (*auth.Claims, error) {
	if s.exchanger == nil {
		return nil, errors.New("oauth code flow: exchanger not wired")
	}
	if s.validator == nil {
		return nil, errors.New("oauth code flow: validator not wired")
	}
	if redirectURI == "" {
		return nil, errors.New("oauth code flow: redirect_uri required")
	}

	tokens, err := s.exchanger.Exchange(ctx, code, redirectURI, codeVerifier)
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

// BeginOAuth issues a server-signed `state` token for the FE to embed
// in the authorize URL. The token is bound to (provider, redirect_uri)
// and short-lived (10 min by default). On callback, Authenticate
// verifies the same token before exchanging the code, blocking CSRF
// attempts that would otherwise complete only on FE state checks.
//
// Returns ErrUnimplemented when the operator hasn't wired
// SetOAuthStateSigner — dev/test stacks fall back to the FE-only state
// validation that's always been there.
func (s *Service) BeginOAuth(_ context.Context, provider, redirectURI string) (string, error) {
	if s.oauthState == nil {
		return "", errors.New("oauth-state: signer not wired")
	}
	return s.oauthState.Mint(provider, redirectURI)
}

// Logout revokes the session family associated with the given refresh
// token AND adds the caller's access token jti to the revocation list
// (when accessToken is non-empty). Idempotent: revoking an
// already-revoked token is a no-op.
//
// The access token half is best-effort — failure here never fails the
// logout, since the token will expire naturally within AccessTokenTTL.
func (s *Service) Logout(ctx context.Context, req *gen.LogoutRequest, accessToken string) error {
	w := wool.Get(ctx).In("Logout")

	if s.minter == nil {
		return w.NewError("auth path not wired: minter missing")
	}
	if accessToken != "" {
		if err := s.minter.RevokeAccess(ctx, accessToken); err != nil {
			w.Warn("RevokeAccess failed (best-effort)", wool.ErrField(err))
		}
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
