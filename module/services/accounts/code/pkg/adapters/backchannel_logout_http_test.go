package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// backchannelStore answers the three reads and writes the back-channel path
// makes. Everything else is the embedded nil interface: a case that reached
// another method would panic rather than quietly pass.
type backchannelStore struct {
	business.Store
	user      *gen.User
	notFound  bool
	revoked   []string
	revokedAs string
}

func (s *backchannelStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *backchannelStore) FindUserByIdentity(context.Context, string, string) (*gen.User, error) {
	if s.notFound {
		return nil, business.NewStoreError(errNoSuchIdentity, business.ErrTypeNotFound)
	}
	return s.user, nil
}

func (s *backchannelStore) RevokeAllUserSessions(_ context.Context, userID, reason string) ([]string, error) {
	s.revokedAs = reason
	return s.revoked, nil
}

var errNoSuchIdentity = &staticError{"identity not found"}

type staticError struct{ message string }

func (e *staticError) Error() string { return e.message }

// logoutTokenVerifier admits one scripted token, standing in for the OIDC
// validator whose own refusals are covered in pkg/auth/oidc.
type logoutTokenVerifier struct {
	token *auth.LogoutToken
	err   error
}

func (v logoutTokenVerifier) ValidateLogoutToken(context.Context, string) (*auth.LogoutToken, error) {
	if v.err != nil {
		return nil, v.err
	}
	return v.token, nil
}

func backchannelHandler(t *testing.T, store *backchannelStore, signOut business.ProviderSignOut) http.Handler {
	t.Helper()
	svc, err := business.NewService(store)
	require.NoError(t, err)
	svc.SetProviderSignOut(signOut)
	return NewBackchannelLogoutHandler(svc)
}

func postLogoutToken(handler http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, BackchannelLogoutPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func subjectToken() *auth.LogoutToken {
	return &auth.LogoutToken{
		Provider: "test-provider",
		Subject:  "user_01ABCXYZ",
		ID:       "logout-token-1",
		IssuedAt: time.Now(),
	}
}

// Back-Channel Logout 1.0 §2.7: a bare 200, no caching, and the sessions gone.
func TestBackchannelLogoutRevokesAndAnswers200(t *testing.T) {
	store := &backchannelStore{
		user:    &gen.User{Uuid: "00000000-0000-7000-8000-00000000000a"},
		revoked: []string{"session-1", "session-2"},
	}
	handler := backchannelHandler(t, store, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	response := postLogoutToken(handler, "logout_token=a.b.c")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Equal(t, "backchannel_logout", store.revokedAs)
}

// A provider subject this host has never seen revokes nothing and still
// succeeds. The provider is entitled to send the token, and whether this host
// knows that person must not be learnable from the response by an
// unauthenticated caller.
func TestBackchannelLogoutSucceedsForAnUnknownIdentity(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{notFound: true}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	require.Equal(t, http.StatusOK, postLogoutToken(handler, "logout_token=a.b.c").Code)
}

// Idempotent by construction: a replayed token revokes an already-revoked set,
// which is zero rows, and that is a success. §2.7 requires the operation to be
// safely repeatable and this is why it needs no replay marker.
func TestBackchannelLogoutIsIdempotent(t *testing.T) {
	store := &backchannelStore{user: &gen.User{Uuid: "00000000-0000-7000-8000-00000000000a"}}
	handler := backchannelHandler(t, store, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	require.Equal(t, http.StatusOK, postLogoutToken(handler, "logout_token=a.b.c").Code)
	require.Equal(t, http.StatusOK, postLogoutToken(handler, "logout_token=a.b.c").Code)
}

// A refused token answers 400 with no cause. The endpoint needs no credential
// to reach, so a body naming why a forgery failed would let it be refined
// against the response.
func TestBackchannelLogoutRefusalNamesNoCause(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{err: auth.ErrNotALogoutToken},
	})

	response := postLogoutToken(handler, "logout_token=a.b.c")
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, "logout token not accepted\n", response.Body.String())
	require.NotContains(t, response.Body.String(), auth.ErrNotALogoutToken.Error())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}

// A verified token naming only a provider session id is a request this host
// cannot carry out. 501 says so; 200 would claim the sessions are gone.
func TestBackchannelLogoutAnswers501ForASessionOnlyToken(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: &auth.LogoutToken{
			Provider: "test-provider", SessionID: "provider-session-7", IssuedAt: time.Now(),
		}},
	})

	require.Equal(t, http.StatusNotImplemented, postLogoutToken(handler, "logout_token=a.b.c").Code)
}

// The request shape §2.5 fixes, and only it: a form POST carrying
// logout_token. A GET, an empty body, a body with no such parameter and a body
// too large to be a logout token are each refused without reaching the
// verifier.
func TestBackchannelLogoutRefusesEveryOtherRequestShape(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	get := httptest.NewRequest(http.MethodGet, BackchannelLogoutPath, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, http.MethodPost, recorder.Header().Get("Allow"))

	require.Equal(t, http.StatusBadRequest, postLogoutToken(handler, "").Code)
	require.Equal(t, http.StatusBadRequest, postLogoutToken(handler, "id_token_hint=a.b.c").Code)
	require.Equal(t, http.StatusBadRequest, postLogoutToken(handler, "logout_token="+strings.Repeat("x", 17<<10)).Code)
}

// A logout token in the query string is not accepted. A credential there lands
// in access logs, proxy logs and referrers; §2.5 puts it in the body and so
// does this.
func TestBackchannelLogoutIgnoresAQueryStringToken(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	request := httptest.NewRequest(http.MethodPost, BackchannelLogoutPath+"?logout_token=a.b.c", strings.NewReader(""))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

// The route is registered as an exact path and the handler holds it to one:
// RegisterHTTPRoute matches by prefix, so anything below the path must not be
// served from here.
func TestBackchannelLogoutServesOnlyItsOwnPath(t *testing.T) {
	handler := backchannelHandler(t, &backchannelStore{}, business.ProviderSignOut{
		LogoutTokens: logoutTokenVerifier{token: subjectToken()},
	})

	request := httptest.NewRequest(http.MethodPost, BackchannelLogoutPath+"/anything", strings.NewReader("logout_token=a.b.c"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}
