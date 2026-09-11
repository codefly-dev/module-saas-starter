package infra

import (
	"accounts/pkg/business"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVaultSecretCipherVersionedPurposeBoundRoundTrip(t *testing.T) {
	var encryptedPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "test-token", r.Header.Get("X-Vault-Token"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/transit/encrypt/api-keys":
			encryptedPayload = body["plaintext"]
			_, _ = fmt.Fprint(w, `{"data":{"ciphertext":"vault:v7:test-ciphertext"}}`)
		case "/v1/transit/decrypt/api-keys":
			require.Equal(t, "vault:v7:test-ciphertext", body["ciphertext"])
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"plaintext": encryptedPayload}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewVaultClientDirect(server.URL, "test-token")
	envelope, err := client.EncryptSecret(t.Context(), "mfa-totp", "TOP-SECRET-SEED")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(envelope, secretEnvelopePrefix))
	require.NotContains(t, envelope, "TOP-SECRET-SEED")

	plaintext, err := client.DecryptSecret(t.Context(), "mfa-totp", envelope)
	require.NoError(t, err)
	require.Equal(t, "TOP-SECRET-SEED", plaintext)

	_, err = client.DecryptSecret(t.Context(), "different-purpose", envelope)
	require.ErrorContains(t, err, "purpose mismatch")
	require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
}

func TestVaultSecretCipherRejectsLegacyPlaintextAndMalformedEnvelope(t *testing.T) {
	client := NewVaultClientDirect("http://unused.invalid", "token")
	_, err := client.DecryptSecret(t.Context(), "mfa-totp", "PLAINTEXTBASE32")
	require.ErrorContains(t, err, "unsupported secret envelope")
	require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)

	_, err = client.DecryptSecret(t.Context(), "mfa-totp", secretEnvelopePrefix+base64.RawURLEncoding.EncodeToString(nil))
	require.ErrorContains(t, err, "invalid secret envelope")
	require.ErrorIs(t, err, business.ErrInvalidSecretEnvelope)
}

func TestVaultDecryptOutageIsNotAnInvalidCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewVaultClientDirect(server.URL, "token")
	envelope := secretEnvelopePrefix + base64.RawURLEncoding.EncodeToString([]byte("vault:v1:ciphertext"))
	_, err := client.DecryptSecret(t.Context(), "datasource", envelope)
	require.Error(t, err)
	require.NotErrorIs(t, err, business.ErrInvalidSecretEnvelope)
}

func TestVaultDecryptExposesStatusWithoutProviderBody(t *testing.T) {
	for _, code := range []int{400, 403, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = fmt.Fprint(w, "sensitive-provider-body")
		}))
		client := NewVaultClientDirect(server.URL, "token")
		_, err := client.DecryptSecret(t.Context(), "purpose", secretEnvelopePrefix+base64.RawURLEncoding.EncodeToString([]byte("vault:v1:old")))
		var typed interface{ HTTPStatusCode() int }
		require.True(t, errors.As(err, &typed))
		require.Equal(t, code, typed.HTTPStatusCode())
		require.NotContains(t, err.Error(), "sensitive-provider-body")
		server.Close()
	}
}
