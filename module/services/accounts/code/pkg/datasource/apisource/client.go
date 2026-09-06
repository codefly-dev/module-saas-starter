// Package apisource is the generic "HTTP API with a stored credential"
// datasource connector: it fetches a tenant-configured resource URL, presenting
// the source's stored credential as a bearer token, basic auth, or a named
// header, and returns the response bytes for the ingest inbox. It holds no
// persistence or crypto concerns of its own — the business layer decrypts the
// credential and hands it in per fetch.
//
// The base URL is tenant-supplied, so every dial is guarded against SSRF: a URL
// that resolves to a private, loopback, link-local, or otherwise non-public
// address is refused, on the initial request and on any redirect, checked
// against the exact IP being connected to (not the hostname) so DNS rebinding
// cannot slip past the check.
package apisource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Credential kinds. They mirror saas.accounts.v1.ApiCredentialKind and select
// how the stored credential is presented on the outbound request. OAuth 2.0 is
// not a presentation kind here: the business layer refreshes an OAuth source's
// access token and hands this connector a bearer credential.
const (
	CredentialKindBearer = "bearer"
	CredentialKindBasic  = "basic"
	CredentialKindHeader = "header"
	CredentialKindQuery  = "query"
)

// maxResponseBytes bounds one fetched response so a pathological endpoint cannot
// exhaust memory or overflow the downstream job payload limit.
const maxResponseBytes = 5 * 1024 * 1024

const maxRedirects = 3

// ErrBlockedAddress is returned when a request target (or a redirect) resolves
// to a non-public IP address. It is deliberately opaque so a tenant cannot use
// the connector to map the cluster's internal network.
var ErrBlockedAddress = errors.New("apisource: request target is not a public address")

// ErrRefreshRejected reports that the token endpoint permanently rejected the
// refresh token (OAuth 2.0 invalid_grant / invalid_client, or a 401). Retrying
// with the same refresh token cannot succeed — the source needs to be
// reconnected — so the caller must treat it as terminal, not transient.
var ErrRefreshRejected = errors.New("apisource: oauth2 refresh token rejected")

// Config is the non-secret configuration of one API source. It mirrors
// saas.accounts.v1.ApiDatasourceConfig.
type Config struct {
	BaseURL              string
	ResourcePath         string
	CredentialKind       string
	CredentialHeader     string
	CredentialQueryParam string
}

// Result is one fetched resource: the raw response body and its declared
// content type, handed to the ingest inbox verbatim.
type Result struct {
	Body        []byte
	ContentType string
}

// Client fetches a single API source's configured resource.
type Client struct {
	cfg        Config
	credential string
	http       *http.Client
}

// New returns a Client for cfg, authenticating with the plaintext credential.
// The HTTP client blocks connections to non-public addresses.
func New(cfg Config, credential string) *Client {
	return &Client{cfg: cfg, credential: credential, http: guardedClient()}
}

// guardedClient builds an HTTP client that refuses connections to non-public
// addresses, on the initial request and every redirect. Both the fetch client
// and the OAuth token exchange dial tenant-supplied hosts, so both use it.
func guardedClient() *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: guardDial,
		}).DialContext,
	}
	return &http.Client{
		Timeout:       30 * time.Second,
		Transport:     transport,
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect bounds the redirect chain and refuses any redirect that leaves
// the originally requested host. net/http strips only Authorization on a
// cross-host redirect — a query-string or custom-header credential, and the
// form body of the OAuth token exchange, would otherwise be resent verbatim to
// a host the tenant did not name. Same-host redirects stay allowed.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("apisource: too many redirects")
	}
	if req.URL.Host != via[0].URL.Host {
		return errors.New("apisource: cross-host redirect blocked")
	}
	return nil
}

// Fetch retrieves the configured resource, applying the source's credential.
func (c *Client) Fetch(ctx context.Context) (*Result, error) {
	target, err := resolveURL(c.cfg.BaseURL, c.cfg.ResourcePath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("apisource: build request: %w", err)
	}
	if err := c.applyCredential(req); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apisource: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("apisource: fetch returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("apisource: read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("apisource: response exceeds %d bytes", maxResponseBytes)
	}
	return &Result{Body: body, ContentType: resp.Header.Get("Content-Type")}, nil
}

func (c *Client) applyCredential(req *http.Request) error {
	switch c.cfg.CredentialKind {
	case CredentialKindBearer:
		req.Header.Set("Authorization", "Bearer "+c.credential)
	case CredentialKindBasic:
		req.Header.Set("Authorization", "Basic "+c.credential)
	case CredentialKindHeader:
		name := strings.TrimSpace(c.cfg.CredentialHeader)
		if name == "" {
			return errors.New("apisource: header credential kind requires a header name")
		}
		req.Header.Set(name, c.credential)
	case CredentialKindQuery:
		name := strings.TrimSpace(c.cfg.CredentialQueryParam)
		if name == "" {
			return errors.New("apisource: query credential kind requires a parameter name")
		}
		q := req.URL.Query()
		q.Set(name, c.credential)
		req.URL.RawQuery = q.Encode()
	default:
		return fmt.Errorf("apisource: unknown credential kind %q", c.cfg.CredentialKind)
	}
	return nil
}

// resolveURL joins the base URL and resource path and requires an absolute
// http(s) URL with a host.
func resolveURL(baseURL, resourcePath string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("apisource: invalid base url: %w", err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", errors.New("apisource: base url must be http or https")
	}
	if base.Host == "" {
		return "", errors.New("apisource: base url must have a host")
	}
	ref, err := url.Parse(strings.TrimSpace(resourcePath))
	if err != nil {
		return "", fmt.Errorf("apisource: invalid resource path: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

// OAuth2Config is the non-secret configuration of an OAuth 2.0 refresh-token
// exchange. It mirrors saas.accounts.v1.ApiOAuth2Config.
type OAuth2Config struct {
	TokenURL string
	ClientID string
	Scopes   []string
}

// OAuth2Token is the outcome of a refresh exchange. RefreshToken is set only
// when the provider rotated it; a provider that keeps the same refresh token
// returns it empty.
type OAuth2Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

// RefreshOAuth2 exchanges a refresh token for a fresh access token at the
// configured token endpoint. The endpoint is tenant-supplied, so the dial is
// SSRF-guarded exactly as Fetch is.
func RefreshOAuth2(ctx context.Context, cfg OAuth2Config, refreshToken, clientSecret string) (*OAuth2Token, error) {
	return (&oauth2Refresher{http: guardedClient()}).refresh(ctx, cfg, refreshToken, clientSecret)
}

type oauth2Refresher struct{ http *http.Client }

func (r *oauth2Refresher) refresh(ctx context.Context, cfg OAuth2Config, refreshToken, clientSecret string) (*OAuth2Token, error) {
	tokenURL := strings.TrimSpace(cfg.TokenURL)
	if tokenURL == "" {
		return nil, errors.New("apisource: oauth2 token url is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	if cfg.ClientID != "" {
		form.Set("client_id", cfg.ClientID)
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("apisource: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apisource: token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("apisource: read token response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("apisource: token response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if refreshPermanentlyRejected(resp.StatusCode, body) {
			return nil, fmt.Errorf("apisource: token exchange returned status %d: %w", resp.StatusCode, ErrRefreshRejected)
		}
		return nil, fmt.Errorf("apisource: token exchange returned status %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("apisource: decode token response: %w", err)
	}
	if payload.AccessToken == "" {
		return nil, errors.New("apisource: token response has no access_token")
	}
	return &OAuth2Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresIn:    time.Duration(payload.ExpiresIn) * time.Second,
	}, nil
}

// refreshPermanentlyRejected classifies a non-2xx token response as a permanent
// rejection of the refresh token rather than a transient failure. A 401, or the
// RFC 6749 error-response codes that mean the grant/client can never succeed
// again, are terminal; any other status (429, 5xx, network) stays retryable.
func refreshPermanentlyRejected(statusCode int, body []byte) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	if statusCode != http.StatusBadRequest && statusCode != http.StatusForbidden {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	switch payload.Error {
	case "invalid_grant", "invalid_client", "unauthorized_client":
		return true
	default:
		return false
	}
}

// guardDial refuses to open a connection to a non-public address. It runs after
// DNS resolution with the concrete IP:port being dialed, so it blocks the exact
// address the connection would reach — closing the DNS-rebinding gap a
// hostname-only check leaves open.
func guardDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ErrBlockedAddress
	}
	return nil
}
