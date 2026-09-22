package githubconnector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrUserCodeRejected is the single answer to every failed user-token exchange:
// an unknown, expired, already-redeemed or mismatched code. One error for all of
// them keeps the endpoint from reporting which codes exist.
var ErrUserCodeRejected = fmt.Errorf("github rejected the user authorization code")

// ExchangeUserCode trades the authorization code GitHub appends to the setup
// redirect for a user-to-server token — a token that acts as the human who came
// back from the install, not as the App.
//
// This is the only way the host can learn *who* is holding the redirect. The
// app JWT proves an installation exists; it says nothing about who controls it.
func (c *Connector) ExchangeUserCode(ctx context.Context, clientID, clientSecret, code string) (string, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	}
	endpoint := c.oauthBaseURL + "/login/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// GitHub answers a rejected code with HTTP 200 and an `error` member rather
	// than a status code, so a successful transport says nothing on its own.
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("exchange user code: %w", err)
	}
	if out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return "", ErrUserCodeRejected
	}
	return out.AccessToken, nil
}

// ErrAccountAdministrationUnreadable means GitHub refused to answer whether the
// caller administers the account, rather than answering "no". The App's
// user-to-server token needs the organization `members: read` permission to ask;
// without it the question cannot be decided, so the caller must fail closed
// rather than read a refusal as an absence of authority.
var ErrAccountAdministrationUnreadable = fmt.Errorf("github did not permit reading the caller's account administration")

// UserAdministersAccount reports whether the user behind userToken administers
// the account an installation belongs to.
//
// This is deliberately not "can the user see the installation". `GET
// /user/installations` answers that, and it includes every installation reachable
// through ordinary organization membership — so it would let any member of an
// organization claim that organization's installation for a tenant they control.
// Only an administrator of the account may install or reconfigure an App on it,
// so administration is the property that entitles a caller to claim it.
//
// A personal-account installation has exactly one administrator, the account
// itself, so there it is an identity comparison.
func (c *Connector) UserAdministersAccount(ctx context.Context, userToken, accountLogin, accountType string) (bool, error) {
	if strings.TrimSpace(accountLogin) == "" {
		return false, fmt.Errorf("administers account: installation carries no account login")
	}
	if strings.EqualFold(accountType, AccountTypeUser) {
		login, err := c.authenticatedUserLogin(ctx, userToken)
		if err != nil {
			return false, err
		}
		return strings.EqualFold(login, accountLogin), nil
	}

	endpoint := fmt.Sprintf("%s/user/memberships/orgs/%s", c.baseURL, url.PathEscape(accountLogin))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	setGitHubHeaders(req)

	var out struct {
		Role  string `json:"role"`
		State string `json:"state"`
	}
	if err := c.do(req, &out); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			// Not a member at all: a decided "no", not a failure to ask.
			if apiErr.StatusCode == http.StatusNotFound {
				return false, nil
			}
			if apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusUnauthorized {
				return false, ErrAccountAdministrationUnreadable
			}
		}
		return false, fmt.Errorf("read organization membership: %w", err)
	}
	// An invited-but-unaccepted admin is `pending`, and does not yet administer.
	return strings.EqualFold(out.Role, "admin") && strings.EqualFold(out.State, "active"), nil
}

// authenticatedUserLogin resolves who a user-to-server token acts as.
func (c *Connector) authenticatedUserLogin(ctx context.Context, userToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	setGitHubHeaders(req)

	var out struct {
		Login string `json:"login"`
	}
	if err := c.do(req, &out); err != nil {
		return "", fmt.Errorf("get authenticated user: %w", err)
	}
	return out.Login, nil
}
