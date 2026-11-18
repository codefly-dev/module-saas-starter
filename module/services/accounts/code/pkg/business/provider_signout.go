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
	var revoked []RevokedSession
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
	// Revoking the rows ends refreshing; the access half is ended by the
	// markers, which are written only now that the revocation has committed.
	s.revokeSessionAccess(ctx, accessRevocationKeys(revoked))
	w.Info("provider back-channel logout applied",
		wool.Field("provider", token.Provider), wool.Field("sessions", len(revoked)))
	return len(revoked), nil
}

// SessionRevokedUserNotActive is the reason recorded on the session rows of an
// identity that is suspended or deleted. It is the value the status trigger
// writes for any row left to it (invalidate_authorization_sessions), and the
// one the refresh path reads back as auth.RefreshRejectionUserNotActive.
const SessionRevokedUserNotActive = auth.RefreshRejectionUserNotActive

// accessRevocationKeys lists the marker keys that end the access half of the
// revoked sessions: each row's id and each family's, once each. Both, because a
// token minted before its family's last refresh carries another session id and
// matches only the family, while an access-only window (impersonation) carries
// no family and matches only its row.
func accessRevocationKeys(revoked []RevokedSession) []string {
	keys := make([]string, 0, 2*len(revoked))
	seen := make(map[string]bool, 2*len(revoked))
	for _, session := range revoked {
		for _, key := range []string{session.ID, session.FamilyID} {
			if key != "" && !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// revokeSessionAccess writes the access-revocation marker for each key, the
// one write every path that ends somebody else's sessions makes after its
// database revocation has committed. The gateway and VerifyAccess admit an
// already-issued access token from these markers alone, so a path that revokes
// rows and skips this leaves the token usable until it expires.
//
// Best-effort, and the same on every caller: the committed rows are the durable
// authority, so a revocation-store outage is logged and bounds the residual
// exposure to the access-token lifetime rather than failing — or rolling back —
// the revocation that already happened.
func (s *Service) revokeSessionAccess(ctx context.Context, keys []string) {
	if s.minter == nil {
		return
	}
	w := wool.Get(ctx).In("revokeSessionAccess")
	for _, key := range keys {
		if err := s.minter.RevokeSessionAccess(ctx, key); err != nil {
			w.Warn("RevokeSessionAccess failed (best-effort)", wool.ErrField(err))
		}
	}
}

// revokeInvalidatedSessionAccess ends the access tokens of the sessions a
// committed change of authority revoked for each user. The database revokes
// those rows itself, from the invalidation triggers on memberships, platform
// roles, second factors and scoped roles, and reports none of them, so this
// reads them back once the change has committed and hands them to the one
// marker write (revokeSessionAccess). Call it after the transaction, never
// inside it: a marker for a change that rolled back would sign out somebody
// whose authority did not move.
//
// Best-effort like the marker write itself. The revoked rows are the durable
// authority and already end refreshing; a failed read bounds the residual
// exposure to the access-token lifetime rather than failing a change that has
// already happened.
func (s *Service) revokeInvalidatedSessionAccess(ctx context.Context, userIDs ...string) {
	if s.minter == nil {
		return
	}
	w := wool.Get(ctx).In("revokeInvalidatedSessionAccess")
	var revoked []RevokedSession
	for _, userID := range userIDs {
		if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
			sessions, err := s.store.InvalidatedUserSessions(ctx, userID)
			revoked = append(revoked, sessions...)
			return err
		}); err != nil {
			w.Warn("cannot read the sessions an authority change revoked (best-effort)", wool.ErrField(err))
		}
	}
	s.revokeSessionAccess(ctx, accessRevocationKeys(revoked))
}
