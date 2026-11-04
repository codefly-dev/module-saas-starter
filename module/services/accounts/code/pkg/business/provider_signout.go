package business

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/auth"
)

// LogoutTokenVerifier admits an OpenID Connect back-channel logout token. The
// generic OIDC validator implements it; a provider adapter that publishes no
// JWKS implements none, and back-channel logout is then unavailable rather than
// unverified.
type LogoutTokenVerifier interface {
	ValidateLogoutToken(ctx context.Context, token string) (*auth.LogoutToken, error)
}

// ProviderSignOut is what this deployment's identity provider offers for ending
// a session at the provider, resolved once at startup. Both halves are
// independently optional, because providers offer them independently: a
// provider may publish an `end_session_endpoint` and no back-channel logout, or
// the reverse.
type ProviderSignOut struct {
	// EndSessionEndpoint is the provider's RP-initiated logout endpoint, from
	// its discovery document or an explicit configuration pin. Empty means the
	// provider offers no RP-initiated sign-out.
	EndSessionEndpoint string
	// ClientID is this relying party's client id, sent as `client_id` when no
	// `id_token_hint` is available (RP-Initiated Logout 1.0 §2).
	ClientID string
	// PostLogoutRedirectURI is where the provider returns the person's browser
	// after ending its session. It is this host's own configured value, never a
	// caller-supplied one: the provider requires it to be pre-registered, and
	// taking it from the request would make the host's own sign-out an open
	// redirect. Empty means none is sent, and the provider decides where the
	// person lands.
	PostLogoutRedirectURI string
	// LogoutTokens verifies back-channel logout tokens. Nil means the endpoint
	// is not mounted at all.
	LogoutTokens LogoutTokenVerifier
}

// SetProviderSignOut wires the provider's sign-out surface. Called once at
// startup, before any request is served.
func (s *Service) SetProviderSignOut(signOut ProviderSignOut) { s.providerSignOut = signOut }

// ProviderSignOutConfigured reports whether a back-channel logout token can be
// verified on this deployment, which is what decides whether the endpoint is
// mounted. An endpoint that cannot verify a token must not exist: it would be
// an unauthenticated route whose only honest answer is a refusal.
func (s *Service) ProviderSignOutConfigured() bool { return s.providerSignOut.LogoutTokens != nil }

// endSessionURL builds the provider's RP-initiated logout URL, or returns ""
// when this deployment has no `end_session_endpoint`. idTokenHint is the
// client-supplied provider id_token; when it is absent the `client_id`
// parameter identifies the relying party instead, which §2 permits precisely
// for that case.
//
// A malformed configured endpoint returns "" with the reason logged rather than
// an error to the caller: the host session has already ended by the time this
// runs, and failing the sign-out over a parse of operator configuration would
// turn a complete local sign-out into a reported failure.
func (s *Service) endSessionURL(ctx context.Context, idTokenHint string) string {
	endpoint := strings.TrimSpace(s.providerSignOut.EndSessionEndpoint)
	if endpoint == "" {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		wool.Get(ctx).In("endSessionURL").Warn("the configured end_session_endpoint is not an absolute URL; no RP-initiated logout URL is returned",
			wool.Field("endpoint", endpoint))
		return ""
	}
	query := parsed.Query()
	if hint := strings.TrimSpace(idTokenHint); hint != "" {
		query.Set("id_token_hint", hint)
	} else if clientID := strings.TrimSpace(s.providerSignOut.ClientID); clientID != "" {
		query.Set("client_id", clientID)
	}
	if redirect := strings.TrimSpace(s.providerSignOut.PostLogoutRedirectURI); redirect != "" {
		query.Set("post_logout_redirect_uri", redirect)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// ErrProviderSessionNotResolvable reports a verified back-channel logout token
// this host cannot act on: the provider named only its own session id (`sid`),
// and nothing here records provider session ids, so no host session can be
// matched to it. It is a verified request the host must answer honestly rather
// than acknowledge as done.
var ErrProviderSessionNotResolvable = errors.New("backchannel logout: the token names only a provider session id, which this host does not record")

// BackchannelLogout verifies an OpenID Connect back-channel logout token and
// ends every host session of the person it names. It returns how many sessions
// it revoked; zero is a success — a person with no live session here is exactly
// the state the provider is asking for, and the endpoint is required to be
// idempotent, so a replayed token revokes nothing and still succeeds.
//
// The token's `sub` is the provider subject, resolved through the same
// (provider, provider_id) pair sign-in writes, so a token for an identity this
// host has never seen revokes nothing rather than erroring: the provider is
// entitled to send one, and whether this host knows that person is not
// something an unauthenticated caller should be able to learn from the
// response.
//
// Replay is deliberately not tracked by `jti`. The operation is idempotent by
// construction — revoking an already-revoked session changes nothing — and the
// verifier bounds a token's age, so a replay window needs no durable marker
// and this endpoint needs no table.
func (s *Service) BackchannelLogout(ctx context.Context, rawToken string) (int, error) {
	w := wool.Get(ctx).In("BackchannelLogout")
	if s.providerSignOut.LogoutTokens == nil {
		return 0, fmt.Errorf("backchannel logout: no identity provider on this deployment verifies logout tokens")
	}
	token, err := s.providerSignOut.LogoutTokens.ValidateLogoutToken(ctx, rawToken)
	if err != nil {
		return 0, err
	}
	if token.Subject == "" {
		w.Warn("a verified logout token named only a provider session id",
			wool.Field("provider", token.Provider), wool.Field("jti", token.ID))
		return 0, ErrProviderSessionNotResolvable
	}
	var revoked []string
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		// The provider subject is not the caller's own identity — there is no
		// caller identity at all on this path — so the lookup and the write both
		// run under the control plane, as every other cross-user session write
		// does. RLS would otherwise scope `sessions` and `user_identities` to a
		// request principal this path does not have.
		user, err := s.store.FindUserByIdentity(ctx, token.Provider, token.Subject)
		if err != nil {
			var storeErr *StoreError
			if errors.As(err, &storeErr) && storeErr.StoreErrorType == ErrTypeNotFound {
				return nil
			}
			return err
		}
		revoked, err = s.store.RevokeAllUserSessions(ctx, user.Uuid, "backchannel_logout")
		return err
	}); err != nil {
		return 0, w.Wrapf(err, "revoke sessions for a provider back-channel logout")
	}
	// Revoking the rows ends refreshing; the access half dies at its natural
	// TTL unless each revoked session is marked. Best-effort for the same
	// reason the platform-admin revoke is: the durable rows above are the
	// authority, and a revocation-store outage must bound the residual exposure
	// rather than discard a sign-out the provider already considers done.
	if s.minter != nil {
		for _, sessionID := range revoked {
			if err := s.minter.RevokeSessionAccess(ctx, sessionID); err != nil {
				w.Warn("RevokeSessionAccess failed (best-effort)", wool.ErrField(err))
			}
		}
	}
	w.Info("provider back-channel logout applied",
		wool.Field("provider", token.Provider), wool.Field("sessions", len(revoked)))
	return len(revoked), nil
}
