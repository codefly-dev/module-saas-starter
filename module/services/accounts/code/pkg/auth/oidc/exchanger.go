package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Exchanger converts an OAuth 2.0 authorization code into an access token
// + id token at the provider's token endpoint. Used by /auth/login during
// the authorization-code + PKCE flow:
//
//  1. Browser redirects to provider's authorize URL.
//  2. Provider redirects back to our /auth/callback with ?code=...
//  3. Frontend POSTs {code, redirect_uri} to backend /auth/login.
//  4. Backend Exchanger.Exchange(code, redirect_uri) → access_token.
//  5. Backend Validator.Validate(access_token) → Claims.
//  6. Backend IdentityResolver.Resolve(Claims) → Identity.
//  7. Backend JWTMinter.Mint(Identity) → our own JWT.
//  8. Backend returns {access_token, refresh_token} to frontend.
//
// Implementations are provider-specific only in URL and body shape —
// the generic OAuth 2.0 code-grant is the same everywhere.
type Exchanger struct {
	tokenURL     string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// ExchangerConfig is the input to NewExchanger. All fields required.
type ExchangerConfig struct {
	TokenURL     string        // e.g. "https://api.workos.com/user_management/authenticate"
	ClientID     string        // OAuth client id
	ClientSecret string        // OAuth client secret (kept on the backend)
	HTTPClient   *http.Client  // optional; default is http.DefaultClient
	Timeout      time.Duration // optional; default 10s
}

// NewExchanger constructs an Exchanger.
func NewExchanger(cfg ExchangerConfig) (*Exchanger, error) {
	if cfg.TokenURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("oidc: TokenURL, ClientID, and ClientSecret are required")
	}
	client := cfg.HTTPClient
	if client == nil {
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	return &Exchanger{
		tokenURL:     cfg.TokenURL,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		httpClient:   client,
	}, nil
}

// TokenResponse is the subset of the standard OAuth 2.0 token response
// we care about. Providers may return additional fields; we ignore them.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// Exchange POSTs the code to the provider's token endpoint using the
// standard OAuth 2.0 grant_type=authorization_code flow.
//
// redirectURI MUST match the one originally passed in the authorize URL;
// providers reject the exchange otherwise.
//
// codeVerifier is the PKCE secret the FE generated; empty means the FE
// did not use PKCE (legacy clients). When non-empty, it is forwarded
// as the standard `code_verifier` form parameter — the provider hashes
// it and compares with the `code_challenge` sent in the authorize URL.
// Including code_verifier alongside client_secret is harmless and is
// the recommended belt-and-suspenders for confidential clients.
func (e *Exchanger) Exchange(ctx context.Context, code, redirectURI, codeVerifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", e.clientID)
	form.Set("client_secret", e.clientSecret)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oidc: build exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oidc: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: token endpoint http %d: %s", resp.StatusCode, string(body))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oidc: parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("oidc: token response missing access_token")
	}
	return &tr, nil
}
