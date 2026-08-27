package githubconnector

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// appJWTClockSkew backdates the app JWT's iat to tolerate clock drift between
// this host and GitHub; appJWTTTL is well under GitHub's 10-minute ceiling.
const (
	appJWTClockSkew = time.Minute
	appJWTTTL       = 9 * time.Minute
)

// InstallationToken is a short-lived GitHub App installation access token and
// the instant it stops being valid.
type InstallationToken struct {
	Token     string
	ExpiresAt time.Time
}

// MintInstallationToken exchanges the App credential for an installation access
// token: it signs a short-lived app JWT with the app's private key and POSTs to
// the installation's access-tokens endpoint. The returned token authorizes REST
// calls scoped to that installation.
func (c *Connector) MintInstallationToken(ctx context.Context, cred AppCredential) (InstallationToken, error) {
	appJWT, err := c.appJWT(cred)
	if err != nil {
		return InstallationToken{}, err
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", c.baseURL, cred.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return InstallationToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	setGitHubHeaders(req)

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.do(req, &out); err != nil {
		return InstallationToken{}, fmt.Errorf("mint installation token: %w", err)
	}
	if out.Token == "" {
		return InstallationToken{}, fmt.Errorf("mint installation token: response missing token")
	}
	return InstallationToken{Token: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

// appJWT builds the RS256-signed JWT that authenticates as the GitHub App
// itself (iss = app id), the credential GitHub requires to mint installation
// tokens.
func (c *Connector) appJWT(cred AppCredential) (string, error) {
	key, err := cred.signingKey()
	if err != nil {
		return "", err
	}
	now := c.now()
	claims := jwt.RegisteredClaims{
		Issuer:    cred.AppID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-appJWTClockSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(appJWTTTL)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign github app jwt: %w", err)
	}
	return signed, nil
}
