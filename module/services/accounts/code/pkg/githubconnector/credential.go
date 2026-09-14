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
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// InstallationScope narrows a minted installation token below the authority the
// installation itself holds: to named repositories ("name", not "owner/name")
// and to an explicit permission set. GitHub refuses a permission the
// installation was not granted, so a caller requests only what it reads.
type InstallationScope struct {
	Repositories []string          `json:"repositories,omitempty"`
	Permissions  map[string]string `json:"permissions,omitempty"`
}

// AppCredential is the GitHub App credential from which an installation access
// token is minted. AppID and InstallationID identify the app and its
// installation on the source's org/repos; PrivateKeyPEM is the app's RSA
// signing key (the sensitive part, PKCS#1 or PKCS#8 PEM).
type AppCredential struct {
	AppID          string `json:"app_id"`
	InstallationID string `json:"installation_id"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	// Scope narrows the minted token to what the caller actually reads; nil
	// mints a token carrying the installation's full authority.
	Scope *InstallationScope `json:"scope,omitempty"`
	// Binding identifies the stored credential this token is minted for. It must
	// be distinct for every binding ever created, because it is what keeps a
	// cached token from outliving the binding it was minted under: GitHub
	// documents no mid-life invalidation when an installation's repository
	// selection or permissions are narrowed, so a token issued under the old
	// binding keeps working until it expires. A counter is the wrong shape here
	// — anything that can return to a previous value re-collides with a live
	// cache entry — so callers pass a value that only moves forward.
	Binding string `json:"binding,omitempty"`
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

// cacheKey identifies the exact authority a minted token carries, so a cached
// token is never handed to a different app, installation, repository or
// permission set, nor survives the credential revision it was minted under.
// Scope components are sorted, so two equivalent scopes share one cache entry.
func (c AppCredential) cacheKey() string {
	var key strings.Builder
	key.WriteString(c.AppID)
	key.WriteString("/")
	key.WriteString(c.InstallationID)
	key.WriteString("#")
	key.WriteString(c.Binding)
	if c.Scope == nil {
		return key.String()
	}
	repos := slices.Clone(c.Scope.Repositories)
	slices.Sort(repos)
	for _, repo := range repos {
		key.WriteString("|r:")
		key.WriteString(repo)
	}
	permissions := make([]string, 0, len(c.Scope.Permissions))
	for name, level := range c.Scope.Permissions {
		permissions = append(permissions, name+"="+level)
	}
	slices.Sort(permissions)
	for _, permission := range permissions {
		key.WriteString("|p:")
		key.WriteString(permission)
	}
	return key.String()
}
