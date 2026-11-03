package oidc

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"accounts/pkg/auth"
)

// maxLogoutTokenAge bounds how old a logout token may be. The specification
// gives no lifetime and providers commonly omit `exp`, so a token with no
// expiry would otherwise be replayable for ever against an endpoint that needs
// no other credential. Two minutes is the same order as the clock skew the
// validator already tolerates on a sign-in token.
const maxLogoutTokenAge = 2 * time.Minute

// ValidateLogoutToken verifies a back-channel logout token and returns what it
// asserts. It reuses the validator's JWKS cache, signing-algorithm allowlist,
// issuer and audience binding — the same key material and the same relying-party
// binding that admits a sign-in — and adds the five checks that separate a
// logout token from every other token the same issuer signs
// (OpenID Connect Back-Channel Logout 1.0 §2.6):
//
//  1. the signature, `iss` and `aud` bindings the validator already enforces;
//  2. an `events` claim carrying auth.BackchannelLogoutEvent;
//  3. a `sub`, a `sid`, or both — a token asserting neither names no session;
//  4. no `nonce` claim, which is how an id_token substituted for a logout token
//     is detected;
//  5. an `iat` within maxLogoutTokenAge, so a token the provider gave no `exp`
//     is not replayable for ever.
//
// Every refusal is one error kind to its caller: the endpoint this serves needs
// no credential beyond the token, so distinguishing "wrong signature" from "not
// a logout token" in the response would tell an unauthenticated caller how far
// its forgery got.
func (v *Validator) ValidateLogoutToken(ctx context.Context, token string) (*auth.LogoutToken, error) {
	// `exp` is not required: §2.4 lists it as OPTIONAL, and the iat bound below
	// is what actually limits the token's life. WithExpirationRequired here
	// would refuse every provider that omits it.
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(v.cfg.AllowedAlgs),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.cfg.ClockSkew),
	}
	if v.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.cfg.Audience))
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.NewParser(opts...).ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if !algAllowed(v.cfg.AllowedAlgs, t.Method.Alg()) {
			return nil, auth.ErrNotALogoutToken
		}
		kid, _ := t.Header["kid"].(string)
		return v.resolveKey(ctx, kid)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", auth.ErrNotALogoutToken, err)
	}
	if !parsed.Valid {
		return nil, auth.ErrNotALogoutToken
	}
	// The audience binding this validator may be configured with instead of
	// `aud` (a provider whose tokens carry a non-standard client_id claim). It
	// is applied here for the same reason it is applied to a sign-in token: a
	// validator with no audience binding admits any correctly-signed token from
	// the issuer, whichever relying party it was meant for, and New refuses to
	// be built that way.
	if v.cfg.ClientIDClaim != "" {
		clientID, _ := claims[v.cfg.ClientIDClaim].(string)
		if clientID == "" || clientID != v.cfg.ClientID {
			return nil, fmt.Errorf("%w: audience binding", auth.ErrNotALogoutToken)
		}
	}
	if !hasBackchannelLogoutEvent(claims["events"]) {
		return nil, fmt.Errorf("%w: no %s event", auth.ErrNotALogoutToken, auth.BackchannelLogoutEvent)
	}
	if _, present := claims["nonce"]; present {
		return nil, fmt.Errorf("%w: a nonce claim is an id_token's, not a logout token's", auth.ErrNotALogoutToken)
	}
	subject, _ := claims["sub"].(string)
	sessionID, _ := claims["sid"].(string)
	if subject == "" && sessionID == "" {
		return nil, fmt.Errorf("%w: neither sub nor sid", auth.ErrNotALogoutToken)
	}
	issuedAtClaim, err := claims.GetIssuedAt()
	if err != nil || issuedAtClaim == nil {
		return nil, fmt.Errorf("%w: no iat", auth.ErrNotALogoutToken)
	}
	issuedAt := issuedAtClaim.Time
	if time.Since(issuedAt) > maxLogoutTokenAge+v.cfg.ClockSkew {
		return nil, fmt.Errorf("%w: issued %s ago", auth.ErrNotALogoutToken, time.Since(issuedAt).Round(time.Second))
	}
	tokenID, _ := claims["jti"].(string)
	return &auth.LogoutToken{
		Provider:  v.cfg.ProviderName,
		Subject:   subject,
		SessionID: sessionID,
		ID:        tokenID,
		IssuedAt:  issuedAt,
	}, nil
}

// hasBackchannelLogoutEvent reports whether the `events` claim carries the
// back-channel logout member. §2.4 requires the member's value to be an empty
// JSON object; a provider that puts something in it is still unambiguously
// requesting a back-channel logout, so the member's presence is what is
// checked and its contents are ignored rather than made a refusal.
func hasBackchannelLogoutEvent(events any) bool {
	members, ok := events.(map[string]any)
	if !ok {
		return false
	}
	_, present := members[auth.BackchannelLogoutEvent]
	return present
}
