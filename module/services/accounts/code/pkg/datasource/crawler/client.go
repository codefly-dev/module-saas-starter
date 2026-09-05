// Package crawler is the generic "documentation website" datasource connector:
// it ingests a site by reading its sitemap.xml, then fetching each listed page,
// returning the page bytes for the ingest inbox. It is a PULL connector with no
// credential — it holds no persistence or crypto concerns of its own.
//
// The sitemap URL and every page URL are tenant-supplied, so every dial is
// guarded against SSRF: a URL that resolves to a private, loopback, link-local,
// or otherwise non-public address is refused, on the sitemap request, on every
// page request, and on any redirect, checked against the exact IP being
// connected to (not the hostname) so DNS rebinding cannot slip past the check.
package crawler

import (
	"context"
	"encoding/xml"
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

// maxResponseBytes bounds one fetched response (the sitemap or a page) so a
// pathological endpoint cannot exhaust memory or overflow the downstream job
// payload limit.
const maxResponseBytes = 5 * 1024 * 1024

const maxRedirects = 3

// defaultMaxPages caps a crawl when Config.MaxPages is not set. maxPagesCeiling
// is a hard ceiling that clamps even an explicit MaxPages, so a tenant cannot
// request an unbounded crawl.
const (
	defaultMaxPages = 50
	maxPagesCeiling = 200
)

// ErrBlockedAddress is returned when a request target (or a redirect) resolves
// to a non-public IP address. It is deliberately opaque so a tenant cannot use
// the connector to map the cluster's internal network.
var ErrBlockedAddress = errors.New("crawler: request target is not a public address")

// Config is the non-secret configuration of one crawler source.
type Config struct {
	SitemapURL string // absolute http(s) URL of a sitemap.xml
	MaxPages   int    // 0 means the default cap
}

// Page is one crawled document: the fetched page bytes and its content type.
type Page struct {
	URL         string
	Body        []byte
	ContentType string
}

// Client crawls a single documentation site's sitemap.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a Client for cfg. The HTTP client blocks connections to non-public
// addresses.
func New(cfg Config) *Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: dialGuard,
		}).DialContext,
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("crawler: too many redirects")
				}
				return nil
			},
		},
	}
}

// Fetch reads the sitemap, then fetches each listed URL, returning one Page per
// successfully fetched document. Pages that individually fail (non-2xx, too
// large, network error) are skipped, not fatal — a single bad page must not
// abort the whole crawl. A failing sitemap fetch is a returned error.
func (c *Client) Fetch(ctx context.Context) ([]Page, error) {
	locs, err := c.fetchSitemap(ctx)
	if err != nil {
		return nil, err
	}

	limit := c.pageLimit()
	pages := make([]Page, 0, len(locs))
	for _, loc := range locs {
		if len(pages) >= limit {
			break
		}
		page, ok := c.fetchPage(ctx, loc)
		if !ok {
			continue
		}
		pages = append(pages, page)
	}
	return pages, nil
}

// pageLimit resolves the number of <loc> entries to fetch, applying the default
// when MaxPages is unset and clamping to the hard ceiling.
func (c *Client) pageLimit() int {
	limit := c.cfg.MaxPages
	if limit <= 0 {
		limit = defaultMaxPages
	}
	if limit > maxPagesCeiling {
		limit = maxPagesCeiling
	}
	return limit
}

// fetchSitemap retrieves and parses the sitemap, returning the trimmed loc URLs.
func (c *Client) fetchSitemap(ctx context.Context) ([]string, error) {
	sitemapURL, err := requireHTTPURL(c.cfg.SitemapURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("crawler: build sitemap request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("crawler: fetch sitemap: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("crawler: sitemap fetch returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("crawler: read sitemap: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("crawler: sitemap exceeds %d bytes", maxResponseBytes)
	}

	var doc struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("crawler: parse sitemap: %w", err)
	}
	locs := make([]string, 0, len(doc.URLs))
	for _, u := range doc.URLs {
		loc := strings.TrimSpace(u.Loc)
		if loc != "" {
			locs = append(locs, loc)
		}
	}
	return locs, nil
}

// fetchPage retrieves one page. It reports ok=false — to be skipped — on any
// individual failure: a non-http(s) loc, a network error, a non-2xx status, or
// an over-large body.
func (c *Client) fetchPage(ctx context.Context, loc string) (Page, bool) {
	pageURL, err := requireHTTPURL(loc)
	if err != nil {
		return Page{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return Page{}, false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Page{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Page{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Page{}, false
	}
	if len(body) > maxResponseBytes {
		return Page{}, false
	}
	return Page{
		URL:         pageURL,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
	}, true
}

// requireHTTPURL validates that raw is an absolute http(s) URL with a host and
// returns its normalized form.
func requireHTTPURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("crawler: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("crawler: url must be http or https")
	}
	if u.Host == "" {
		return "", errors.New("crawler: url must have a host")
	}
	return u.String(), nil
}

// dialGuard refuses to open a connection to a non-public address. It runs after
// DNS resolution with the concrete IP:port being dialed, so it blocks the exact
// address the connection would reach — closing the DNS-rebinding gap a
// hostname-only check leaves open. It is a package-level var so tests can hit a
// loopback httptest server; production always uses the guarding implementation.
var dialGuard = func(_, address string, _ syscall.RawConn) error {
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
