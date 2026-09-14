// Package executioncustody is the versioned private broker wire/client contract.
// It intentionally contains no issuer, storage or owner impersonation capability.
package executioncustody

import (
	"bytes"
	"context"
	"crypto/tls"
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

// HTTPStatus is the v1 broker status for this code, or zero for an unknown code.
func (e *Error) HTTPStatus() int {
	switch e.Code {
	case "InvalidArgument":
		return http.StatusBadRequest
	case "Unauthenticated":
		return http.StatusUnauthorized
	case "PermissionDenied":
		return http.StatusForbidden
	case "NotFound":
		return http.StatusNotFound
	case "AlreadyExists":
		return http.StatusConflict
	case "FailedPrecondition":
		return http.StatusPreconditionFailed
	case "Unavailable":
		return http.StatusServiceUnavailable
	default:
		return 0
	}
}

// Client never retries a command or follows a redirect carrying credentials.
// For worker exchange Transport must present the configured client certificate.
type Client struct {
	endpoint  string
	http      *http.Client
	transport *http.Transport
}

func NewClient(endpoint string, transport *http.Transport) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || transport == nil || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.DialTLS != nil || transport.DialTLSContext != nil || len(transport.TLSNextProto) != 0 {
		return nil, errors.New("verified HTTPS broker transport required")
	}
	t := transport.TLSClientConfig
	if t.InsecureSkipVerify || t.MinVersion < tls.VersionTLS13 || t.VerifyConnection != nil || t.VerifyPeerCertificate != nil || (t.MaxVersion != 0 && t.MaxVersion < t.MinVersion) {
		return nil, errors.New("verified TLS 1.3 broker transport required")
	}
	tr := transport.Clone()
	return &Client{endpoint: endpoint, transport: tr, http: &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }
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
		if decode(resp.Body, 4096, &failure) != nil {
			return &Error{Code: "Unavailable"}
		}
		if failure.HTTPStatus() != resp.StatusCode {
			return &Error{Code: "Unavailable"}
		}
		return &failure
	}
	if decode(resp.Body, 128<<10, out) != nil {
		return &Error{Code: "Unavailable"}
	}
	return nil
}

func decode(body io.Reader, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return errors.New("bounded v1 response required")
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("v1 response object required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("complete v1 response required")
	}
	return nil
}
