package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// The browser's logout posts an empty body and the HttpOnly cookie. The cookie
// was moved into the body for refresh and not for logout, so field validation
// rejected the request before Logout ran: the session family stayed active and
// the access-token id was never revoked, while the browser's own cookie was
// cleared — a sign-out that looked complete and ended nothing.
func TestL1LogoutForwardsCookieRefreshToken(t *testing.T) {
	handler := refreshTokenCookie(downstream(`{}`))

	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: refreshTokenCookieName, Value: "cookie-rt"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Contains(t, recorder.Header().Get("X-Echo-Body"), "cookie-rt",
		"logout must reach the handler carrying the cookie's refresh token")

	cleared := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == refreshTokenCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	require.True(t, cleared, "logout must still clear the browser's cookie")
}

// A logout that already carries a token in its body keeps it: the cookie is a
// fallback for the browser, not an override of an explicit credential.
func TestLogoutKeepsAnExplicitBodyRefreshToken(t *testing.T) {
	handler := refreshTokenCookie(downstream(`{}`))

	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout",
		strings.NewReader(`{"refresh_token":"body-rt"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	require.Contains(t, recorder.Header().Get("X-Echo-Body"), "body-rt")
}

// Every REST route that completes an authentication hands the refresh credential
// to the browser as a cookie and strips it from the body. The set is derived
// from the service descriptor rather than restated, so a route added to the
// contract cannot be left out of the middleware: the WebAuthn completion was
// exactly that, sharing its response message with the TOTP completion while only
// the TOTP path was listed.
func TestEveryAuthCompletionCookiesTheRefreshToken(t *testing.T) {
	declared := restRoutesReturningARefreshToken(t)
	require.NotEmpty(t, declared, "the descriptor scan found no route to check")

	require.Contains(t, declared, oauthTokenEndpointRESTPath,
		"the exemption names a route the contract does not have")

	for path := range declared {
		if path == oauthTokenEndpointRESTPath {
			continue
		}
		t.Run(path, func(t *testing.T) {
			require.True(t, authenticationCompletingRESTPaths[path],
				"this route's response carries a refresh token and must be cookie-lifted")

			handler := refreshTokenCookie(downstream(`{"accessToken":"at","refreshToken":"rt-secret"}`))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))

			var body map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.NotContains(t, body, "refreshToken")
			require.NotContains(t, body, "refresh_token")
			require.NotContains(t, recorder.Body.String(), "rt-secret")

			cookie := refreshCookieOf(t, recorder)
			require.Equal(t, "rt-secret", cookie.Value)
			require.True(t, cookie.HttpOnly)
			require.True(t, cookie.Secure)
			require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
			require.Equal(t, "/v1/auth", cookie.Path)
		})
	}

	// And nothing is listed that the contract does not actually return a refresh
	// token on, so the map cannot be kept green by growing it.
	for path := range authenticationCompletingRESTPaths {
		require.Contains(t, declared, path,
			"this path is cookie-lifted but no descriptor route returns a refresh token there")
	}
}

// restRoutesReturningARefreshToken reads the accounts service descriptors and
// returns the POST paths whose response message carries a refresh_token field.
func restRoutesReturningARefreshToken(t *testing.T) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	services := gen.File_saas_accounts_v1_authentication_proto.Services()
	for i := range services.Len() {
		methods := services.Get(i).Methods()
		for j := range methods.Len() {
			method := methods.Get(j)
			if method.Output().Fields().ByName(protoreflect.Name("refresh_token")) == nil {
				continue
			}
			rule, ok := proto.GetExtension(
				method.Options(), annotations.E_Http).(*annotations.HttpRule)
			if !ok || rule == nil {
				continue
			}
			if post := rule.GetPost(); post != "" {
				paths[post] = true
			}
		}
	}
	return paths
}

// The Connect surface of the same methods: it serves the same browser, over the
// same origin, and returned the credential as readable content. A Connect caller
// that needs the token in a body is a registered OAuth client and uses the token
// endpoint, which is a different contract.
func TestConnectAuthCompletionsCookieTheRefreshToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		carry func() (http.Header, string)
	}{
		{"Authenticate", func() (http.Header, string) {
			response := connect.NewResponse(&gen.AuthenticateResponse{AccessToken: "at", RefreshToken: "rt-secret"})
			cookieTheRefreshToken(response.Header(), response.Msg)
			return response.Header(), response.Msg.GetRefreshToken()
		}},
		{"CompleteMFAChallenge", func() (http.Header, string) {
			response := connect.NewResponse(&gen.CompleteMFAChallengeResponse{AccessToken: "at", RefreshToken: "rt-secret"})
			cookieTheRefreshToken(response.Header(), response.Msg)
			return response.Header(), response.Msg.GetRefreshToken()
		}},
		{"RefreshToken", func() (http.Header, string) {
			response := connect.NewResponse(&gen.RefreshTokenResponse{AccessToken: "at", RefreshToken: "rt-secret"})
			cookieTheRefreshToken(response.Header(), response.Msg)
			return response.Header(), response.Msg.GetRefreshToken()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, remaining := tc.carry()
			require.Empty(t, remaining, "the response body must not carry the refresh token")

			setCookie := header.Get("Set-Cookie")
			require.Contains(t, setCookie, refreshTokenCookieName+"=rt-secret")
			require.Contains(t, setCookie, "HttpOnly")
			require.Contains(t, setCookie, "Secure")
			require.Contains(t, setCookie, "SameSite=Strict")
			require.Contains(t, setCookie, "Path=/v1/auth")
		})
	}
}

// A response with no refresh token is left exactly as it was, so the lift cannot
// be satisfied by clearing a field that was never set or by always emitting a
// cookie.
func TestCookieTheRefreshTokenLeavesAnUnrelatedResponseAlone(t *testing.T) {
	response := connect.NewResponse(&gen.AuthenticateResponse{AccessToken: "at"})
	cookieTheRefreshToken(response.Header(), response.Msg)

	require.Equal(t, "at", response.Msg.GetAccessToken())
	require.Empty(t, response.Header().Get("Set-Cookie"))
}

// The handlers have to actually route through the lift. Read the source and
// require it of every authConnectHandler method whose response message carries a
// refresh token, so a handler added later cannot quietly use the plain helper.
func TestEveryConnectAuthCompletionRoutesThroughTheLift(t *testing.T) {
	text := executableSource(t, "connect_handlers.go")

	methods := regexp.MustCompile(
		`(?m)^func \(h \*authConnectHandler\) ([A-Za-z]+)\(ctx context\.Context, req \*connect\.Request\[gen\.([A-Za-z]+)\]\) \(\*connect\.Response\[gen\.([A-Za-z]+)\][^\n]*\n((?:\t[^\n]*\n)*)`).
		FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, methods)

	checked := 0
	for _, match := range methods {
		name, responseType, body := match[1], match[3], match[4]
		message, err := protoregistry.GlobalTypes.FindMessageByName(
			protoreflect.FullName("saas.accounts.v1." + responseType))
		require.NoError(t, err, "%s: unknown response message %s", name, responseType)
		if message.Descriptor().Fields().ByName("refresh_token") == nil ||
			connectHandlersExemptFromTheRefreshCookie[name] {
			continue
		}
		checked++
		require.Contains(t, body, "unaryCookieingRefreshToken",
			"%s completes an authentication and must lift the refresh token out of the body", name)
	}
	require.NotZero(t, checked, "the scan matched no authentication-completing handler")

	// The exemption set is exactly the OAuth token endpoint, so an entry cannot be
	// added to it without this assertion being read and changed.
	require.Equal(t, map[string]bool{"ExchangeClientToken": true},
		connectHandlersExemptFromTheRefreshCookie)
}

// The response half removed the credential from the Connect body; without a request
// half a Connect client had nowhere to put it back, so refresh and logout failed
// field validation before reaching the verifier. A carrier that works in one
// direction is not a carrier.
func TestR1019ConnectRefreshConsumesCookie(t *testing.T) {
	refresh := connect.NewRequest(&gen.RefreshTokenRequest{})
	refresh.Header().Set("Cookie", refreshTokenCookieName+"=cookie-rt; codefly_session=1")
	refreshTokenFromCookie(refresh.Header(), refresh.Msg.GetRefreshToken(), func(token string) {
		refresh.Msg.RefreshToken = token
	})
	require.Equal(t, "cookie-rt", refresh.Msg.GetRefreshToken(),
		"the Connect refresh must take the credential from the cookie")

	logout := connect.NewRequest(&gen.LogoutRequest{})
	logout.Header().Set("Cookie", refreshTokenCookieName+"=cookie-rt")
	refreshTokenFromCookie(logout.Header(), logout.Msg.GetRefreshToken(), func(token string) {
		logout.Msg.RefreshToken = token
	})
	require.Equal(t, "cookie-rt", logout.Msg.GetRefreshToken())
}

// An explicit body credential wins: the cookie is the browser's convenience, not an
// override — the same precedence the REST middleware uses.
func TestR1019ConnectBodyCredentialWinsOverTheCookie(t *testing.T) {
	request := connect.NewRequest(&gen.RefreshTokenRequest{RefreshToken: "body-rt"})
	request.Header().Set("Cookie", refreshTokenCookieName+"=cookie-rt")
	refreshTokenFromCookie(request.Header(), request.Msg.GetRefreshToken(), func(token string) {
		request.Msg.RefreshToken = token
	})
	require.Equal(t, "body-rt", request.Msg.GetRefreshToken())
}

// No cookie and no body leaves the message untouched, so the absence is reported by
// field validation rather than by an empty credential reaching the verifier.
func TestR1019ConnectWithNoCarrierIsUnchanged(t *testing.T) {
	request := connect.NewRequest(&gen.RefreshTokenRequest{})
	request.Header().Set("Cookie", "codefly_session=1")
	refreshTokenFromCookie(request.Header(), request.Msg.GetRefreshToken(), func(token string) {
		request.Msg.RefreshToken = token
	})
	require.Empty(t, request.Msg.GetRefreshToken())
}

// The lift has to run BEFORE the message is validated, or the empty field is
// rejected before the cookie is ever consulted. The handler is the only place that
// ordering holds, so it is asserted from the source.
func TestR1019ConnectRefreshLiftsBeforeValidation(t *testing.T) {
	// Comments stripped: replacing the call with a comment that contains its text
	// satisfied a substring search while nothing consumed the cookie (R1019-N15).
	text := executableSource(t, "connect_handlers.go")

	for _, method := range []string{"RefreshToken", "Logout"} {
		pattern := regexp.MustCompile(`func \(h \*authConnectHandler\) ` + method +
			`\(ctx context\.Context(?s:.*?)\n\}\n`)
		body := pattern.FindString(text)
		require.NotEmpty(t, body, method)
		lift := strings.Index(body, "refreshTokenFromCookie(")
		dispatch := strings.Index(body, "h.inner.")
		require.NotEqual(t, -1, lift, "%s must consume the cookie carrier", method)
		require.Less(t, lift, dispatch,
			"%s must lift the cookie before dispatching, or validation refuses the empty field first",
			method)
	}
}

// R1019-N15: every assertion above either calls cookieTheRefreshToken directly or
// reads the source for the wrapper's NAME, so removing the lift from inside the
// wrapper survived the whole suite — the helper still worked when called by a test,
// and the handlers still named the wrapper that no longer called it.
//
// This drives the wrapper itself. It is the only test that fails if the call inside
// it goes away.
func TestR1019TheLiftRunsInsideTheWrapper(t *testing.T) {
	request := connect.NewRequest(&gen.RefreshTokenRequest{RefreshToken: "rt-in"})

	response, err := unaryCookieingRefreshToken(t.Context(), request,
		func(context.Context, *gen.RefreshTokenRequest) (*gen.RefreshTokenResponse, error) {
			return &gen.RefreshTokenResponse{AccessToken: "at", RefreshToken: "rt-secret"}, nil
		})
	require.NoError(t, err)

	require.Empty(t, response.Msg.GetRefreshToken(),
		"the wrapper must lift the credential out of the body it returns")
	require.Equal(t, "at", response.Msg.GetAccessToken(), "the rest of the response is untouched")
	setCookie := response.Header().Get("Set-Cookie")
	require.Contains(t, setCookie, refreshTokenCookieName+"=rt-secret",
		"the wrapper must put the credential in the cookie it lifted it into")
	require.Contains(t, setCookie, "HttpOnly")
}
