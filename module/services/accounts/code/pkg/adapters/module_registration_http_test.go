package adapters

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingMinter struct {
	prefix string
	err    error
}

func (m *recordingMinter) MintModuleRegistration(prefix string) (string, time.Time, error) {
	if m.err != nil {
		return "", time.Time{}, m.err
	}
	m.prefix = prefix
	return "signed-token-for-" + prefix, time.Unix(1700000000, 0).UTC(), nil
}

func digestOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// newModuleRegistrationHarness installs the internal credential the exchange
// requires and returns a handler declaring one module.
func newModuleRegistrationHarness(t *testing.T, secret string) (http.Handler, *recordingMinter) {
	t.Helper()
	previous := internalToken
	internalToken = "test-internal-token"
	t.Cleanup(func() { internalToken = previous })

	secrets, err := ParseModuleRegistrationSecrets("documents:" + digestOf(secret))
	require.NoError(t, err)
	minter := &recordingMinter{}
	return NewModuleRegistrationHTTPHandler(minter, secrets), minter
}

func exchangeRequest(prefix, secret, internal string) *http.Request {
	body := fmt.Sprintf(`{"prefix":%q,"secret":%q}`, prefix, secret)
	request := httptest.NewRequest(http.MethodPost, ModuleRegistrationPath, strings.NewReader(body))
	if internal != "" {
		request.Header.Set(moduleRegistrationInternalTokenHeader, internal)
	}
	return request
}

func TestModuleRegistrationHandlerMintsForDeclaredModule(t *testing.T) {
	handler, minter := newModuleRegistrationHarness(t, "documents-registration-secret")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, exchangeRequest("documents", "documents-registration-secret", "test-internal-token"))

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "documents", minter.prefix)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))

	var payload struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, "signed-token-for-documents", payload.Token)
	require.Equal(t, "2023-11-14T22:13:20Z", payload.ExpiresAt)
}

// The per-module binding is the property the shared cluster token could not
// give: holding "documents"' secret must not yield a token for another prefix.
func TestModuleRegistrationHandlerBindsSecretToPrefix(t *testing.T) {
	handler, minter := newModuleRegistrationHarness(t, "documents-registration-secret")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, exchangeRequest("billing", "documents-registration-secret", "test-internal-token"))

	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Empty(t, minter.prefix)
}

func TestModuleRegistrationHandlerFailsClosed(t *testing.T) {
	tests := map[string]struct {
		prefix   string
		secret   string
		internal string
	}{
		"wrong secret":           {"documents", "guessed", "test-internal-token"},
		"empty secret":           {"documents", "", "test-internal-token"},
		"undeclared prefix":      {"unknown", "documents-registration-secret", "test-internal-token"},
		"missing internal token": {"documents", "documents-registration-secret", ""},
		"wrong internal token":   {"documents", "documents-registration-secret", "not-the-token"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			handler, minter := newModuleRegistrationHarness(t, "documents-registration-secret")

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, exchangeRequest(test.prefix, test.secret, test.internal))

			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Empty(t, minter.prefix)
		})
	}
}

// A composition that declared nothing must let nobody register, rather than
// treating "no policy" as "any policy".
func TestModuleRegistrationHandlerDeniesWhenNothingDeclared(t *testing.T) {
	previous := internalToken
	internalToken = "test-internal-token"
	t.Cleanup(func() { internalToken = previous })

	secrets, err := ParseModuleRegistrationSecrets("")
	require.NoError(t, err)
	require.Empty(t, secrets)

	handler := NewModuleRegistrationHTTPHandler(&recordingMinter{}, secrets)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, exchangeRequest("documents", "documents-registration-secret", "test-internal-token"))

	require.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestModuleRegistrationHandlerRejectsOtherPathsAndMethods(t *testing.T) {
	handler, _ := newModuleRegistrationHarness(t, "documents-registration-secret")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, ModuleRegistrationPath, nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, ModuleRegistrationPath+"/extra", nil))
	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestModuleRegistrationHandlerSurfacesMintFailure(t *testing.T) {
	previous := internalToken
	internalToken = "test-internal-token"
	t.Cleanup(func() { internalToken = previous })

	secrets, err := ParseModuleRegistrationSecrets("documents:" + digestOf("s3cret"))
	require.NoError(t, err)
	handler := NewModuleRegistrationHTTPHandler(&recordingMinter{err: errors.New("no signing key")}, secrets)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, exchangeRequest("documents", "s3cret", "test-internal-token"))

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestParseModuleRegistrationSecrets(t *testing.T) {
	secrets, err := ParseModuleRegistrationSecrets(
		" documents:" + digestOf("a") + " , billing:" + digestOf("b") + " ,")
	require.NoError(t, err)
	require.Len(t, secrets, 2)
	require.Contains(t, secrets, "documents")
	require.Contains(t, secrets, "billing")

	invalid := map[string]string{
		"missing digest":    "documents",
		"not hex":           "documents:zzzz",
		"wrong digest size": "documents:" + hex.EncodeToString([]byte("short")),
		"path prefix":       "documents/nested:" + digestOf("a"),
		"wildcard prefix":   "*:" + digestOf("a"),
		"duplicate prefix":  "documents:" + digestOf("a") + ",documents:" + digestOf("b"),
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := ParseModuleRegistrationSecrets(raw)
			require.Error(t, err)
		})
	}
}
