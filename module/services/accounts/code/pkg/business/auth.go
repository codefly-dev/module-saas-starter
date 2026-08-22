package business

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// AccessTokenLifetime is the TTL baked into minted access tokens.
// Kept in sync with ed25519minter.Config.AccessTokenTTL so the
// ExpiresIn field on Authenticate responses matches reality.
const AccessTokenLifetime = 3 * time.Minute

// Authenticate runs a login or signup through the identity resolver and
// mints a fresh token pair.
//
// Two explicitly typed paths, selected by the request oneof:
//
//  1. **OAuth code flow** (production): OAuthCodeAuthentication carries the
//     authorization code, redirect URI, signed state, and PKCE verifier. The code is exchanged at the provider's token
//     endpoint via CodeExchanger, the resulting access_token is
//     validated via TokenValidator, and the resulting claims drive
//     JIT provisioning. Requires both Validator and Exchanger to be
//     wired via the setters.
//
//  2. **Fixture path** (development only): FixtureAuthentication carries an opaque token
//     resolved by an explicitly wired development validator. Provider,
//     subject, and email come exclusively from the allowlisted fixture;
//     caller-supplied identity claims are never trusted.
//
// The profile map carries the resolution intent. An "invitation_token" routes
// to the invite flow, which provisions the invitee against a pending
// invitation and joins them to the inviting org. Otherwise the request is a
// signup: a first-seen identity is provisioned, and an "org_name" additionally
// creates their first org with them as owner.
func (s *Service) Authenticate(ctx context.Context, req *gen.AuthenticateRequest) (*gen.AuthenticateResponse, error) {
	w := wool.Get(ctx).In("Authenticate")

	if req == nil {
		return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "authentication request missing")
	}
	if s.resolver == nil || s.minter == nil {
		return nil, w.NewError("auth path not wired: resolver/minter missing")
	}
	deviceDescription := strings.TrimSpace(req.DeviceInfo)
	if len(deviceDescription) > 512 {
		return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "device info exceeds 512 bytes")
	}

	intent := intentFromProfile(req.Profile)

	var claims *auth.Claims
	var err error
	var authenticationMethod string
	fixtureMFAWasVerified := false
	switch credentials := req.Authentication.(type) {
	case *gen.AuthenticateRequest_OauthCode:
		authenticationMethod = auth.AuthenticationMethodOAuth
		oauthCode := credentials.OauthCode
		if oauthCode == nil || oauthCode.Code == "" || oauthCode.RedirectUri == "" || oauthCode.State == "" || oauthCode.CodeVerifier == "" {
			return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "oauth code flow: required credential missing")
		}
		if s.validateOAuthRequest(ctx, req.Provider, oauthCode.RedirectUri) != nil {
			return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "oauth code flow: provider or redirect rejected")
		}
		if s.oauthState == nil {
			return nil, w.Wrapf(auth.ErrInvalidOAuthState, "oauth state signer not configured")
		}
		if err := s.oauthState.Verify(oauthCode.State, req.Provider, oauthCode.RedirectUri); err != nil {
			// Surface the canonical sentinel without leaking whether the
			// signature, expiry, or binding check failed.
			return nil, w.Wrapf(auth.ErrInvalidOAuthState, "oauth state")
		}
		claims, err = s.authenticateWithCode(ctx, req.Provider, oauthCode.Code, oauthCode.RedirectUri, oauthCode.CodeVerifier)
		if err != nil {
			return nil, w.Wrapf(err, "oauth code exchange")
		}
	case *gen.AuthenticateRequest_HeaderJwt:
		authenticationMethod = auth.AuthenticationMethodHeaderJWT
		if s.validator == nil {
			return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "header-jwt authentication is not enabled")
		}
		if credentials.HeaderJwt == nil || credentials.HeaderJwt.Token == "" {
			return nil, w.Wrapf(auth.ErrMissingClaims, "header-jwt token missing")
		}
		claims, err = s.validator.Validate(ctx, credentials.HeaderJwt.Token)
		if err != nil {
			return nil, w.Wrapf(err, "header-jwt authentication")
		}
	case *gen.AuthenticateRequest_Fixture:
		authenticationMethod = auth.AuthenticationMethodFixture
		if s.devValidator == nil {
			return nil, w.Wrapf(auth.ErrDevelopmentAuthDisabled, "fixture authentication is not enabled")
		}
		if credentials.Fixture == nil || credentials.Fixture.Token == "" {
			return nil, w.Wrapf(auth.ErrUnknownIdentity, "development fixture token missing")
		}
		claims, err = s.devValidator.Validate(ctx, credentials.Fixture.Token)
		if err != nil {
			return nil, w.Wrapf(err, "development fixture authentication")
		}
		if req.Provider != "" && claims != nil && claims.Provider != "" && req.Provider != claims.Provider {
			return nil, w.NewError("development fixture authentication: provider mismatch")
		}
		if attestor, ok := s.devValidator.(interface {
			FixtureMFAWasVerified(string) bool
		}); ok {
			fixtureMFAWasVerified = attestor.FixtureMFAWasVerified(credentials.Fixture.Token)
		}
	default:
		return nil, w.Wrapf(auth.ErrInvalidOAuthRequest, "authentication credential required")
	}
	if claims == nil {
		return nil, w.Wrapf(auth.ErrMissingClaims, "provider claims")
	}
	if err := claims.Valid(); err != nil {
		return nil, w.Wrapf(err, "provider claims")
	}
	if err := s.authorizeAuthentication(ctx, claims); err != nil {
		return nil, err
	}

	// An org-bound provider whose org carries a JIT provisioning policy resolves
	// through the sealed SsoJitIntent instead of the profile-derived default.
	// Global-provider logins keep the selection above untouched.
	intent = selectSsoJitIntent(ctx, intent, claims, s.ssoProvisioningRouter())

	// An invite authentication accepts the invitation transactionally inside the
	// resolver. Capture its pre-state here so the business layer — which owns the
	// event pipeline the resolver cannot reach — can emit the accepted side
	// effects on success, and lazily expire it when the resolver fails it closed.
	// An invite-only SSO login consumes an invitation too, but carries no token,
	// so its pre-state is found by (org, email) instead.
	var priorInvitation *Invitation
	switch typed := intent.(type) {
	case auth.InviteIntent:
		priorInvitation, _ = s.invitationByToken(ctx, typed.Token)
	case auth.SsoJitIntent:
		priorInvitation = s.pendingInvitationForSsoJit(ctx, typed.OrgID, claims.Email)
	}

	identity, err := s.resolver.Resolve(ctx, claims, intent)
	if err != nil {
		if priorInvitation != nil && priorInvitation.Status == "pending" && errors.Is(err, ErrInvitationExpired) {
			s.expireInvitation(ctx, priorInvitation)
		}
		return nil, w.Wrapf(err, "identity resolution")
	}
	if priorInvitation != nil && priorInvitation.Status == "pending" {
		s.announceInvitationAccepted(ctx, priorInvitation, identity.UserID.String())
	}
	identity.AuthenticationMethods = []string{authenticationMethod}
	if deviceDescription != "" {
		identity.DeviceInfo = map[string]string{"description": deviceDescription}
	}
	identity.AuthenticatedAt = time.Now()
	identity.AssuranceLevel = auth.AssuranceLevelAAL1
	s.convertWaitlistLead(
		ctx,
		claims.Email,
		identity.UserID.String(),
		identity.OrgID.String(),
	)
	if fixtureMFAWasVerified {
		identity.AuthenticationMethods = append(identity.AuthenticationMethods, auth.AuthenticationMethodOTP)
		identity.AssuranceLevel = auth.AssuranceLevelAAL2
		identity.MFAVerifiedAt = identity.AuthenticatedAt
		identity.MFASatisfied = true
	}

	// MFA gate: users with no enrolled device have nothing to challenge,
	// so the session counts as "MFA satisfied" automatically. Users with
	// a verified device receive only an opaque, durable login transaction;
	// no access token, refresh token, or sessions row exists before the
	// factor challenge succeeds.
	// mfa_devices is RLS-protected by user_id (Phase 2G).
	var enrolled bool
	var user *gen.User
	mErr := s.store.WithUserTx(ctx, identity.UserID.String(), func(ctx context.Context) error {
		e, err := s.store.HasVerifiedMFA(ctx, identity.UserID.String())
		enrolled = e
		if err != nil {
			return err
		}
		user, err = s.store.GetUser(ctx, identity.UserID.String())
		return err
	})
	if mErr != nil {
		return nil, w.Wrapf(mErr, "cannot determine MFA enrollment")
	}

	// Hydrate once for either response path. Failure is non-fatal: identity
	// resolution already established the canonical user id.
	if user == nil {
		user = &gen.User{Uuid: identity.UserID.String()}
	}

	if enrolled && !fixtureMFAWasVerified {
		mfaToken, err := s.BeginMFALogin(ctx, identity)
		if err != nil {
			return nil, w.Wrapf(err, "begin MFA login")
		}
		return &gen.AuthenticateResponse{
			User:        user,
			MfaRequired: true,
			MfaToken:    mfaToken,
		}, nil
	}
	identity.MFASatisfied = true

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
		User:         user,
	}, nil
}

// intentFromProfile maps the request profile to a resolution intent. A pending
// invitation is honoured first so an invitee is always provisioned through the
// invite flow; every other authentication is a signup that provisions a
// first-seen identity. Gating signup behind an access policy is issue #A2.
func intentFromProfile(profile map[string]string) auth.Intent {
	if token := strings.TrimSpace(profile["invitation_token"]); token != "" {
		return auth.InviteIntent{Token: token}
	}
	return auth.SignupIntent{OrganizationName: profile["org_name"]}
}

// ssoProvisioningRouter maps a provider-asserted organization id to our org id
// and reports whether that org has a JIT provisioning policy. The production
// resolver (pkg/auth/pg) satisfies it; a resolver that does not is simply never
// routed to SsoJitIntent.
type ssoProvisioningRouter interface {
	ResolveOrgProvisioning(ctx context.Context, providerOrgID string) (uuid.UUID, bool, error)
}

// ssoProvisioningRouter returns the identity resolver as an SSO router when it
// supports the capability, or nil.
func (s *Service) ssoProvisioningRouter() ssoProvisioningRouter {
	router, _ := s.resolver.(ssoProvisioningRouter)
	return router
}

// selectSsoJitIntent upgrades an org-bound login to SsoJitIntent when the
// asserted org carries a JIT provisioning policy. A global-provider login (no
// asserted org), a token-carried invitation, an absent router, or an org
// without a policy all keep the incoming intent, so global paths are unchanged.
// A router error is non-fatal: the login falls back to the incoming intent
// (the profile-derived Signup/Invite). That fallback does not reproduce the
// org's policy — a first-seen org-bound identity is still rejected, but by the
// resolver's own org-membership check rather than the mode-specific error — so a
// transient lookup failure degrades to a retryable rejection, never to a login
// that bypasses the policy.
func selectSsoJitIntent(ctx context.Context, intent auth.Intent, claims *auth.Claims, router ssoProvisioningRouter) auth.Intent {
	if router == nil || claims.ProviderOrgID == "" {
		return intent
	}
	if _, isInvite := intent.(auth.InviteIntent); isInvite {
		return intent
	}
	orgID, hasPolicy, err := router.ResolveOrgProvisioning(ctx, claims.ProviderOrgID)
	if err != nil || !hasPolicy {
		return intent
	}
	return auth.SsoJitIntent{OrgID: orgID}
}

// authenticateWithCode runs the full OAuth authorization-code flow:
// exchange code → validate access token → extract claims. Requires both
// CodeExchanger and TokenValidator to be wired.
//
// codeVerifier is the FE-generated PKCE secret. Forwarded to the provider's token endpoint so PKCE binds
// the code redemption to the original authorize request.
func (s *Service) authenticateWithCode(ctx context.Context, provider, code, redirectURI, codeVerifier string) (*auth.Claims, error) {
	if s.exchanger == nil {
		return nil, errors.New("oauth code flow: exchanger not wired")
	}
	if redirectURI == "" {
		return nil, errors.New("oauth code flow: redirect_uri required")
	}

	tokens, err := s.exchanger.Exchange(ctx, code, redirectURI, codeVerifier)
	if err != nil {
		return nil, err
	}

	claims := tokens.Claims
	if claims == nil {
		if s.validator == nil {
			return nil, errors.New("oauth code flow: validator not wired")
		}
		// Prefer id_token when present (standard OIDC); fall back to access_token.
		tokenToValidate := tokens.IDToken
		if tokenToValidate == "" {
			tokenToValidate = tokens.AccessToken
		}

		claims, err = s.validator.Validate(ctx, tokenToValidate)
		if err != nil {
			return nil, err
		}
	}
	if claims == nil {
		return nil, auth.ErrMissingClaims
	}
	if err := claims.Valid(); err != nil {
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
// revokes every active session for that user via the strict OWASP rotation
// pattern.
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

// SwitchOrganization exchanges the current, verified device-session context
// for a fresh access token scoped to another organization. The database owns
// membership and role resolution; the request supplies only the target id.
// Refresh state is preserved, so this operation neither creates a device nor
// participates in refresh-token reuse detection.
func (s *Service) SwitchOrganization(
	ctx context.Context,
	userID string,
	sessionID uuid.UUID,
	req *gen.SwitchOrganizationRequest,
) (*gen.SwitchOrganizationResponse, error) {
	w := wool.Get(ctx).In("SwitchOrganization")
	if s.minter == nil {
		return nil, w.NewError("auth path not wired: minter missing")
	}
	parsedUserID, err := uuid.Parse(userID)
	if err != nil || sessionID == uuid.Nil || req == nil {
		return nil, w.Wrapf(auth.ErrSessionUnavailable, "verified session context missing")
	}
	targetOrgID, err := uuid.Parse(req.OrganizationId)
	if err != nil {
		return nil, w.Wrapf(auth.ErrOrganizationAccessDenied, "invalid target organization")
	}

	accessToken, err := s.minter.SwitchOrganization(ctx, parsedUserID, sessionID, targetOrgID)
	if err != nil {
		return nil, w.Wrapf(err, "organization token exchange")
	}

	s.emit(ctx, userID, "user", "auth.organization_switched", "organization", req.OrganizationId, req.OrganizationId)
	return &gen.SwitchOrganizationResponse{
		AccessToken: accessToken,
		ExpiresIn:   int64(AccessTokenLifetime.Seconds()),
	}, nil
}

// validateOAuthRequest authorizes both halves of the OAuth flow — initiation
// and the callback code exchange — so the two can never disagree about which
// redirect is acceptable.
//
// At the Codefly frontend entry there is no operator-registered redirect list:
// the gateway authenticates the browser origin and Accounts binds the callback
// to it, so the only valid target is that origin's own /auth/callback. A direct
// request that never crossed the trusted frontend boundary carries no verified
// origin and stays restricted to the static allowlist, which is empty unless an
// operator deliberately configures IDENTITY_ALLOWED_REDIRECT_URIS.
func (s *Service) validateOAuthRequest(ctx context.Context, provider, redirectURI string) error {
	if s.oauthPolicy == nil {
		return auth.ErrInvalidOAuthRequest
	}
	if publicOrigin, ok := auth.VerifiedPublicOrigin(ctx); ok {
		if s.oauthPolicy.ValidateForPublicOrigin(provider, redirectURI, publicOrigin) != nil {
			return auth.ErrInvalidOAuthRequest
		}
		return nil
	}
	if s.oauthPolicy.Validate(provider, redirectURI) != nil {
		return auth.ErrInvalidOAuthRequest
	}
	return nil
}

// BeginOAuth issues a server-signed `state` token for the FE to embed
// in the authorize URL. The token is bound to (provider, redirect_uri)
// and short-lived (10 min by default). On callback, Authenticate
// verifies the same token before exchanging the code, blocking CSRF
// attempts that would otherwise complete only on FE state checks.
func (s *Service) BeginOAuth(ctx context.Context, provider, redirectURI string) (string, error) {
	if err := s.validateOAuthRequest(ctx, provider, redirectURI); err != nil {
		return "", err
	}
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
