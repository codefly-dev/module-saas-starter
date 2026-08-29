package githubconnector

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultBaseURL is the public GitHub REST API host. GitHub Enterprise Server
// installations override it via WithBaseURL.
const DefaultBaseURL = "https://api.github.com"

// tokenRefreshWindow re-mints an installation token this long before it
// actually expires, so a token handed out for a fetch never lapses mid-request.
const tokenRefreshWindow = time.Minute

// Connector authenticates as a GitHub App installation and pulls repository
// contents. It caches minted installation tokens per installation (they are
// valid for an hour) so a sync that fetches many files does not re-mint on
// every request or hit GitHub's token-creation rate limit. It is safe for
// concurrent use.
type Connector struct {
	baseURL    string
	httpClient *http.Client
	now        func() time.Time

	mu      sync.Mutex
	tokens  map[string]InstallationToken
	minting singleflight.Group
}

// Option configures a Connector.
type Option func(*Connector)

// WithBaseURL points the connector at a non-default API host (GitHub Enterprise
// Server, or a test server).
func WithBaseURL(baseURL string) Option {
	return func(c *Connector) {
		if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
			c.baseURL = trimmed
		}
	}
}

// WithClock overrides the time source, for deterministic token-expiry tests.
func WithClock(now func() time.Time) Option {
	return func(c *Connector) {
		if now != nil {
			c.now = now
		}
	}
}

// NewConnector builds a connector against api.github.com unless overridden.
func NewConnector(opts ...Option) *Connector {
	c := &Connector{
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		now:        time.Now,
		tokens:     make(map[string]InstallationToken),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FetchRepoContents authenticates with the source's credential and pulls the
// contents at path. It reuses a cached installation token when one is still
// comfortably valid. This is the entry point SyncSource and webhook re-fetch
// call.
func (c *Connector) FetchRepoContents(ctx context.Context, cred AppCredential, owner, repo, path, ref string) (*RepoContent, error) {
	token, err := c.installationToken(ctx, cred)
	if err != nil {
		return nil, err
	}
	return c.GetRepoContents(ctx, token, owner, repo, path, ref)
}

// installationToken returns a cached token for the credential's installation,
// minting and caching a fresh one when none is cached or the cached one is
// within the refresh window of expiry. Minting is de-duplicated per
// installation: concurrent callers that all miss the cache share a single mint
// rather than each creating a token (GitHub rate-limits token creation).
func (c *Connector) installationToken(ctx context.Context, cred AppCredential) (string, error) {
	key := cred.cacheKey()

	if token, ok := c.cachedToken(key); ok {
		return token, nil
	}

	minted, err, _ := c.minting.Do(key, func() (any, error) {
		// A concurrent leader may have filled the cache while this call waited to
		// become the singleflight leader; re-check before minting.
		if token, ok := c.cachedToken(key); ok {
			return token, nil
		}
		token, err := c.MintInstallationToken(ctx, cred)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.tokens[key] = token
		c.mu.Unlock()
		return token.Token, nil
	})
	if err != nil {
		return "", err
	}
	return minted.(string), nil
}

// cachedToken returns a still-valid cached token for key, if any.
func (c *Connector) cachedToken(key string) (string, bool) {
	c.mu.Lock()
	cached, ok := c.tokens[key]
	c.mu.Unlock()
	if ok && c.now().Before(cached.ExpiresAt.Add(-tokenRefreshWindow)) {
		return cached.Token, true
	}
	return "", false
}
