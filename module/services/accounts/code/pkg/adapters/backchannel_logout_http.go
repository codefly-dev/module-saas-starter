package adapters

import (
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/business"
)

// The OpenID Connect back-channel logout receiver.
//
// A raw HTTP handler rather than a proto RPC for the same reason the OAuth
// surface is one: the shape IS the contract. Back-Channel Logout 1.0 §2.5
// requires the relying party to accept
// `application/x-www-form-urlencoded` with a single `logout_token` parameter,
// and §2.7 requires a bare 200 on success and a 400 with no body of substance
// on a token it refuses. grpc-gateway produces neither, so a provider's own
// client — which is what calls this, not any code of ours — could not use a
// transcoded RPC.
//
// The endpoint is unauthenticated at the perimeter and that is correct: the
// caller is the identity provider's back channel, which holds no session, no
// bearer and no API key here. The signed logout token is the credential, and it
// is verified against the provider's JWKS with the same key material, issuer and
// audience binding that admits a sign-in. work.go mounts this route only when a
// provider that can verify a logout token is configured
// (business.Service.ProviderSignOutConfigured), so a deployment whose provider
// offers no back-channel logout — the development fixture, every provider with
// no JWKS — exposes no such surface at all rather than one whose only honest
// answer is a refusal.

// BackchannelLogoutPath is where the provider posts a logout token. It is under
// `/v1/auth/` with a name no proto route uses: RegisterHTTPRoute matches by
// prefix and wins over the grpc-gateway mux, so the prefix must be narrow
// enough that it cannot shadow `/v1/auth/logout` or any other transcoded auth
// route, and it is registered as the exact path for that reason.
const BackchannelLogoutPath = "/v1/auth/backchannel-logout"

// maxLogoutTokenBody bounds the body read. A logout token is a compact JWS of a
// few hundred bytes; this leaves room for a generous provider and refuses an
// unauthenticated caller streaming a body at an endpoint that needs no
// credential to reach.
const maxLogoutTokenBody = 16 << 10

// NewBackchannelLogoutHandler serves the provider's back-channel logout
// receiver.
func NewBackchannelLogoutHandler(svc *business.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wl := wool.Get(r.Context()).In("backchannelLogout")
		if r.URL.Path != BackchannelLogoutPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			// §2.5: the request is a form POST. Anything else is not a
			// back-channel logout request at all.
			refuseBackchannelLogout(w, nil)
			return
		}
		token, err := logoutTokenFromRequest(r)
		if err != nil {
			refuseBackchannelLogout(w, nil)
			return
		}
		revoked, err := svc.BackchannelLogout(r.Context(), token)
		switch {
		case errors.Is(err, business.ErrProviderSessionNotResolvable):
			// A verified request this host cannot act on. §2.7 says to answer
			// 501 when the RP does not support the form of logout the token
			// asks for, which is exactly this: the token named a provider
			// session id and nothing here records one. Answering 200 would tell
			// the provider the sessions are gone when they are not.
			wl.Warn("a verified back-channel logout names only a provider session id, which this host cannot match", wool.ErrField(err))
			http.Error(w, "this relying party does not record provider session ids; send a sub claim", http.StatusNotImplemented)
			return
		case err != nil:
			// Everything else — a bad signature, a token that is not a logout
			// token, an unreachable JWKS, a failed revocation — is one answer.
			// The reason is logged; an unauthenticated caller learns only that
			// the token was not acted on, so a forgery cannot be refined
			// against the response.
			wl.Warn("back-channel logout refused", wool.ErrField(err))
			refuseBackchannelLogout(w, err)
			return
		}
		// §2.7: a bare 200 with no cache. Zero revoked sessions is a success —
		// the person has no live session here, which is the state the provider
		// asked for, and a replayed token legitimately revokes nothing.
		wl.Info("back-channel logout applied", wool.Field("sessions", revoked))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	})
}

// logoutTokenFromRequest reads the single `logout_token` form parameter. The
// body is read under a limit and parsed here rather than through
// r.ParseForm, which would also fold in the URL query: a logout token is a
// credential and belongs in the body, where it does not land in an access log
// or a referrer.
func logoutTokenFromRequest(r *http.Request) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLogoutTokenBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxLogoutTokenBody {
		return "", errors.New("backchannel logout: request body is too large to be a logout token")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "", err
	}
	token := values.Get("logout_token")
	if token == "" {
		return "", errors.New("backchannel logout: no logout_token parameter")
	}
	return token, nil
}

// refuseBackchannelLogout answers §2.7's refusal: 400, no caching, and a body
// that names no cause. err is taken only to keep the call sites honest about
// having one to log; it is deliberately not written to the response.
func refuseBackchannelLogout(w http.ResponseWriter, _ error) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "logout token not accepted", http.StatusBadRequest)
}
