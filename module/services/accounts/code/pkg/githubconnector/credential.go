// Package githubconnector authenticates to GitHub on behalf of a datasource
// and pulls repository contents. saas-starter owns "connection"; the Source
// this operates against is defined by the Datasource/Connector contract, so
// callers pass their own source-scoped credential (stored encrypted by the
// business layer) into every operation here.
package githubconnector

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// AppCredential is the per-source GitHub App credential from which an
// installation access token is minted. AppID and InstallationID identify the
// app and its installation on the source's org/repos; PrivateKeyPEM is the
// app's RSA signing key (the sensitive part, PKCS#1 or PKCS#8 PEM). This is the
// plaintext shape that the business layer encrypts through SecretCipher and
// stores as a single envelope.
type AppCredential struct {
	AppID          string `json:"app_id"`
	InstallationID string `json:"installation_id"`
	PrivateKeyPEM  string `json:"private_key_pem"`
}

// Marshal renders the credential as the JSON secret persisted by the store.
func (c AppCredential) Marshal() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseAppCredential decodes the JSON secret produced by Marshal.
func ParseAppCredential(secret string) (AppCredential, error) {
	var c AppCredential
	if err := json.Unmarshal([]byte(secret), &c); err != nil {
		return AppCredential{}, fmt.Errorf("decode github app credential: %w", err)
	}
	if err := c.Validate(); err != nil {
		return AppCredential{}, err
	}
	return c, nil
}

// Validate checks that the credential carries an app id, an installation id,
// and a parseable RSA private key.
func (c AppCredential) Validate() error {
	if strings.TrimSpace(c.AppID) == "" {
		return fmt.Errorf("github app credential requires an app id")
	}
	if strings.TrimSpace(c.InstallationID) == "" {
		return fmt.Errorf("github app credential requires an installation id")
	}
	if _, err := c.signingKey(); err != nil {
		return err
	}
	return nil
}

// signingKey parses the PEM private key. golang-jwt's helper accepts both
// PKCS#1 ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") blocks, the two forms
// GitHub hands out.
func (c AppCredential) signingKey() (*rsa.PrivateKey, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(c.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse github app private key: %w", err)
	}
	return key, nil
}

// cacheKey identifies the installation a minted token belongs to, so a cached
// token is never handed to a different app or installation.
func (c AppCredential) cacheKey() string {
	return c.AppID + "/" + c.InstallationID
}
