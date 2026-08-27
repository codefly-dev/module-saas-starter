package githubconnector_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/githubconnector"
)

// testKeyPEM generates a fresh PKCS#1 RSA private key PEM for a test credential.
func testKeyPEM(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBytes), key
}

func testCredential(t *testing.T) githubconnector.AppCredential {
	t.Helper()
	keyPEM, _ := testKeyPEM(t)
	return githubconnector.AppCredential{
		AppID:          "123456",
		InstallationID: "789",
		PrivateKeyPEM:  keyPEM,
	}
}

func TestAppCredentialMarshalParseRoundTrip(t *testing.T) {
	cred := testCredential(t)

	secret, err := cred.Marshal()
	require.NoError(t, err)

	parsed, err := githubconnector.ParseAppCredential(secret)
	require.NoError(t, err)
	require.Equal(t, cred, parsed)
}

func TestAppCredentialMarshalRejectsIncomplete(t *testing.T) {
	keyPEM, _ := testKeyPEM(t)
	cases := map[string]githubconnector.AppCredential{
		"missing app id":          {InstallationID: "789", PrivateKeyPEM: keyPEM},
		"missing installation id": {AppID: "123", PrivateKeyPEM: keyPEM},
		"missing key":             {AppID: "123", InstallationID: "789"},
		"malformed key":           {AppID: "123", InstallationID: "789", PrivateKeyPEM: "not a pem"},
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := cred.Marshal()
			require.Error(t, err)
		})
	}
}

func TestParseAppCredentialRejectsGarbage(t *testing.T) {
	_, err := githubconnector.ParseAppCredential("{not json")
	require.Error(t, err)
}
