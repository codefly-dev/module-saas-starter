// Package executioncustody is the versioned private broker wire/client contract.
// It intentionally contains no issuer, storage or owner impersonation capability.
package executioncustody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const RegisterPath = "/private/v1/execution-custody/register"
const RecoverPath = "/private/v1/execution-custody/recover"
const ExchangePath = "/private/v1/execution-custody/exchange"

type Binding struct {
	OrgID            string `json:"org_id"`
	OwnerID          string `json:"owner_id"`
	AdmissionID      string `json:"admission_id"`
	IntentDigest     string `json:"intent_digest"`
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id"`
	Consumer         string `json:"consumer"`
	Profile          string `json:"profile"`
	TaskClaimsDigest string `json:"task_claims_digest,omitempty"`
}

type RegisterRequest struct {
	Binding     Binding `json:"binding"`
	ParentToken string  `json:"parent_token"`
	// TaskExpiresAt is an immutable requested upper bound, never a TTL renewed on retry.
	TaskExpiresAt int64 `json:"task_expires_at"`
}

// RecoverRequest contains no bearer. It can only retrieve an existing exact
// admission; absence never creates or signs a replacement registration.
type RecoverRequest struct {
	Binding       Binding `json:"binding"`
	TaskExpiresAt int64   `json:"task_expires_at"`
}

type Registration struct {
	Reference string  `json:"reference"`
	Binding   Binding `json:"binding"`
	ExpiresAt int64   `json:"expires_at"`
	TaskToken string  `json:"task_token"`
}

type ExchangeRequest struct {
	Reference string  `json:"reference"`
	Binding   Binding `json:"binding"`
	Audience  string  `json:"audience"`
	Lookup    bool    `json:"lookup"`
}

// Child returns the canonical signed Work Context token. Consumers MUST verify
// its signature, lineage, exact audience/scopes/profile and sealed horizon.
type Child struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

type Error struct {
	Code string `json:"code"`
}

func (e *Error) Error() string { return "execution custody: " + e.Code }

// Client never retries a command or follows a redirect carrying credentials.
// For worker exchange Transport must present the configured client certificate.
type Client struct {
	endpoint string
	http     *http.Client
}

func NewClient(endpoint string, transport *http.Transport) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || transport == nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("verified HTTPS broker transport required")
	}
	return &Client{endpoint: endpoint, http: &http.Client{Transport: transport.Clone(), Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Register(ctx context.Context, accessToken string, in RegisterRequest) (Registration, error) {
	var out Registration
	err := c.call(ctx, RegisterPath, accessToken, in, &out)
	return out, err
}
func (c *Client) Recover(ctx context.Context, accessToken string, in RecoverRequest) (Registration, error) {
	var out Registration
	err := c.call(ctx, RecoverPath, accessToken, in, &out)
	return out, err
}

func (c *Client) Exchange(ctx context.Context, in ExchangeRequest) (Child, error) {
	var out Child
	err := c.call(ctx, ExchangePath, "", in, &out)
	return out, err
}
func (c *Client) call(ctx context.Context, path, token string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return &Error{Code: "InvalidArgument"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return &Error{Code: "InvalidArgument"}
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Code: "Unavailable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure Error
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure) != nil || failure.Code == "" {
			return &Error{Code: "Unavailable"}
		}
		switch failure.Code {
		case "InvalidArgument", "Unauthenticated", "PermissionDenied", "NotFound", "AlreadyExists", "FailedPrecondition", "Unavailable":
			return &failure
		default:
			return &Error{Code: "Unavailable"}
		}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(out) != nil {
		return &Error{Code: "Unavailable"}
	}
	return nil
}
