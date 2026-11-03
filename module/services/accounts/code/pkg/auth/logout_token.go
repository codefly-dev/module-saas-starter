package auth

import (
	"errors"
	"time"
)

// BackchannelLogoutEvent is the event name a logout token must carry, per
// OpenID Connect Back-Channel Logout 1.0 §2.4. A signed token from the right
// issuer that does not carry it is some other token of the provider's — an
// id_token, most dangerously — and admitting one would turn every sign-in into
// a sign-out request anybody holding the token could replay.
const BackchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// ErrNotALogoutToken reports a correctly-signed token from the configured
// issuer that is not a back-channel logout token, or that is one the
// specification refuses: no subject and no session id, a `nonce` claim (which
// only an id_token carries, and whose presence is how §2.6 says to detect an
// id_token substituted for a logout token), or an events claim without the
// back-channel logout member.
var ErrNotALogoutToken = errors.New("oidc: not a back-channel logout token")

// LogoutToken is the admitted content of a back-channel logout token: who the
// provider is ending a session for, and the token's own identity for replay
// detection. It deliberately carries nothing else — a logout token's other
// claims say nothing this host acts on.
//
// It lives in this package, beside Claims, for the same reason Claims does:
// both are what a provider adapter hands the business layer, and the adapters
// (pkg/auth/oidc) already depend on the business layer, so a type the two
// exchange cannot live in either of them.
type LogoutToken struct {
	// Provider is the configured provider name, the same value written to
	// user_identities.provider, so the caller can resolve Subject without
	// knowing which validator admitted the token.
	Provider string
	// Subject is the provider's `sub`: the person whose sessions end. Empty
	// when the provider sent only a session id.
	Subject string
	// SessionID is the provider's `sid`. Empty when the provider sent only a
	// subject.
	SessionID string
	// ID is the token's `jti`. Empty when the provider sent none.
	ID string
	// IssuedAt is the token's `iat`.
	IssuedAt time.Time
}
