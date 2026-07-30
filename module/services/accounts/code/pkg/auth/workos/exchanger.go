// Package workos implements the provider-specific WorkOS AuthKit exchange.
//
// WorkOS does not return a conventional OIDC id_token from
// /user_management/authenticate. Its signed access token proves the subject
// and session, while the authenticated response's user object carries the
// verified primary email. This adapter joins those two sources only after
// validating the token through the configured JWKS validator.
package workos

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	Validator    auth.TokenValidator
	HTTPClient   *http.Client
	Timeout      time.Duration
}

type Exchanger struct {
	tokenURL     string
	clientID     string
	clientSecret string
	validator    auth.TokenValidator
	httpClient   *http.Client
}

func NewExchanger(cfg Config) (*Exchanger, error) {
	if strings.TrimSpace(cfg.TokenURL) == "" ||
		strings.TrimSpace(cfg.ClientID) == "" ||
		strings.TrimSpace(cfg.ClientSecret) == "" ||
		cfg.Validator == nil {
		return nil, fmt.Errorf("workos: token URL, client ID, client secret, and validator are required")
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
		validator:    cfg.Validator,
		httpClient:   client,
	}, nil
}

type authenticateResponse struct {
	AccessToken    string `json:"access_token"`
	OrganizationID string `json:"organization_id"`
	User           struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	} `json:"user"`
}

func (e *Exchanger) Exchange(
	ctx context.Context,
	code,
	_ string,
	codeVerifier string,
) (business.ExchangedTokens, error) {
	body := map[string]string{
		"client_id":     e.clientID,
		"client_secret": e.clientSecret,
		"grant_type":    "authorization_code",
		"code":          code,
	}
	if codeVerifier != "" {
		body["code_verifier"] = codeVerifier
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: encode authentication request: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		e.tokenURL,
		bytes.NewReader(encoded),
	)
	if err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: build authentication request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: authentication endpoint: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: read authentication response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return business.ExchangedTokens{}, fmt.Errorf(
			"workos: authentication endpoint http %d",
			resp.StatusCode,
		)
	}
	var result authenticateResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: parse authentication response: %w", err)
	}
	if result.AccessToken == "" || result.User.ID == "" || result.User.Email == "" {
		return business.ExchangedTokens{}, fmt.Errorf("workos: authentication response missing identity data")
	}
	if !result.User.EmailVerified {
		return business.ExchangedTokens{}, fmt.Errorf("workos: primary email is not verified")
	}

	claims, err := e.validator.Validate(ctx, result.AccessToken)
	if err != nil {
		return business.ExchangedTokens{}, fmt.Errorf("workos: validate access token: %w", err)
	}
	if claims.Subject != result.User.ID {
		return business.ExchangedTokens{}, fmt.Errorf("workos: access-token subject does not match authenticated user")
	}
	if claims.ProviderOrgID != "" &&
		result.OrganizationID != "" &&
		claims.ProviderOrgID != result.OrganizationID {
		return business.ExchangedTokens{}, fmt.Errorf("workos: access-token organization does not match authentication response")
	}
	claims.Email = strings.ToLower(strings.TrimSpace(result.User.Email))
	if claims.ProviderOrgID == "" {
		claims.ProviderOrgID = result.OrganizationID
	}
	if err := claims.Valid(); err != nil {
		return business.ExchangedTokens{}, err
	}

	return business.ExchangedTokens{
		AccessToken: result.AccessToken,
		Claims:      claims,
	}, nil
}

var _ business.CodeExchanger = (*Exchanger)(nil)
