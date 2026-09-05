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
// how the stored credential is presented on the outbound request.
const (
	CredentialKindBearer = "bearer"
	CredentialKindBasic  = "basic"
	CredentialKindHeader = "header"
)

// maxResponseBytes bounds one fetched response so a pathological endpoint cannot
// exhaust memory or overflow the downstream job payload limit.
const maxResponseBytes = 5 * 1024 * 1024

const maxRedirects = 3

// ErrBlockedAddress is returned when a request target (or a redirect) resolves
// to a non-public IP address. It is deliberately opaque so a tenant cannot use
// the connector to map the cluster's internal network.
var ErrBlockedAddress = errors.New("apisource: request target is not a public address")

// Config is the non-secret configuration of one API source. It mirrors
// saas.accounts.v1.ApiDatasourceConfig.
type Config struct {
	BaseURL          string
	ResourcePath     string
	CredentialKind   string
	CredentialHeader string
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
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: guardDial,
		}).DialContext,
	}
	return &Client{
		cfg:        cfg,
		credential: credential,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("apisource: too many redirects")
				}
				return nil
			},
		},
	}
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
