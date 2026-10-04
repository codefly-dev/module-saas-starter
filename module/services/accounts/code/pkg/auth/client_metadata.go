package auth

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
	"sync"
	"syscall"
	"time"
)

// Client ID Metadata Documents (the OAuth CIMD draft, and the registration
// mechanism the MCP specification prefers since 2026-07-28).
//
// A client whose `client_id` is an https URL publishes its own registration at
// that URL. The host fetches it, validates it, and treats it as a public client
// of the registry for that one flow. Nothing durable is written: there is no
// row, no admin surface, and no state that outlives the authorization request.
// That is the property that makes this acceptable where dynamic client
// registration (RFC 7591) is not — a registry that grows on unauthenticated
// request is a registry an attacker sizes, and a registered name is a name that
// can impersonate. See the handbook's decisions/registered-clients.md.
//
// Trust boundary: the document is fetched from a URL a caller supplied, so
// every fetch is bounded (size, time, redirects) and SSRF-guarded at the dial,
// exactly as the tenant-supplied data-source URLs are.

// Named refusals. Each says which rule the document broke, for operators
// reading logs and for the tests that hold each rule. The public answer stays
// ErrClientAuthorizationRejected: an unauthenticated caller must not learn
// which clients this deployment would admit.
var (
	// ErrClientMetadataDisabled is a metadata client presented to a deployment
	// that has not enabled them.
	ErrClientMetadataDisabled = errors.New("client metadata documents are not enabled")
	// ErrClientMetadataNotAllowed is a client_id URL outside the operator's
	// allowlist.
	ErrClientMetadataNotAllowed = errors.New("client metadata document is not allow-listed")
	// ErrClientMetadataClientID is a client_id that is not a usable https URL.
	ErrClientMetadataClientID = errors.New("client metadata client_id must be an https URL")
	// ErrClientMetadataUnreachable is a document that could not be fetched.
	ErrClientMetadataUnreachable = errors.New("client metadata document is unreachable")
	// ErrClientMetadataTooLarge is a document above the size bound.
	ErrClientMetadataTooLarge = errors.New("client metadata document is too large")
	// ErrClientMetadataRedirected is a fetch redirected off its own origin.
	ErrClientMetadataRedirected = errors.New("client metadata document redirected to another origin")
	// ErrClientMetadataMalformed is a document that is not a JSON object.
	ErrClientMetadataMalformed = errors.New("client metadata document is malformed")
	// ErrClientMetadataIdentityMismatch is a document whose own client_id is not
	// the URL it was served from. Without this check one document could claim
	// any client id, which is the impersonation DCR invites.
	ErrClientMetadataIdentityMismatch = errors.New("client metadata document names a different client_id")
	// ErrClientMetadataAuthMethod is a document that does not declare itself a
	// public client. RFC 7591's default is client_secret_basic, so an absent
	// value is refused rather than read as "none".
	ErrClientMetadataAuthMethod = errors.New("client metadata document must declare token_endpoint_auth_method none")
	// ErrClientMetadataRedirectURIs is a document with no usable redirect URI.
	ErrClientMetadataRedirectURIs = errors.New("client metadata document declares no usable redirect URI")
	// ErrClientMetadataGrantTypes is a document that does not declare the
	// authorization-code grant, or declares a response type other than code.
	ErrClientMetadataGrantTypes = errors.New("client metadata document must declare the authorization_code grant and the code response type")
)

const (
	// maxClientMetadataBytes bounds the body read. A registration document is a
	// few hundred bytes; this is three orders of magnitude of headroom and still
	// far below anything that could pressure the process.
	maxClientMetadataBytes = 64 << 10
	// clientMetadataFetchTimeout bounds one fetch, including connect and body
	// read. An authorize request is a person waiting in a browser.
	clientMetadataFetchTimeout = 5 * time.Second
	// clientMetadataMaxRedirects bounds same-origin redirects. A document moved
	// within its own origin still resolves; a redirect off that origin is
	// refused, because the origin is the client's identity.
	clientMetadataMaxRedirects = 3
	// clientMetadataMinTTL / clientMetadataMaxTTL bound what a document's own
	// Cache-Control may ask for. The floor keeps an aggressive no-store from
	// turning every authorize request into an outbound fetch; the ceiling keeps
	// a client's own rotation of its redirect URIs from taking a day to apply.
	clientMetadataMinTTL = 60 * time.Second
	clientMetadataMaxTTL = time.Hour
	// clientMetadataFailureTTL caches a refusal briefly. Without it an
	// unauthenticated caller can make this host fetch an arbitrary https URL
	// once per request; with it the same bad client_id costs one fetch a minute.
	clientMetadataFailureTTL = 60 * time.Second
)

// ClientMetadataDocument is the subset of the registration document the host
// reads. Unknown members are ignored, as the draft requires — but every member
// the host acts on is read explicitly, never inferred from a default.
type ClientMetadataDocument struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientURI               string   `json:"client_uri"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// ClientMetadataPolicy is the operator's answer to "may a client register
// itself by publishing a document?". It is a deployment-level decision, not a
// per-tenant one: whether this host trusts the CIMD mechanism at all is a
// property of the host, and a per-organisation allowlist would be exactly the
// durable, growing registry this mechanism exists to avoid.
type ClientMetadataPolicy struct {
	// enabled is false for the zero value, so a deployment that configured
	// nothing admits no metadata client.
	enabled bool
	// allowed, when non-empty, is the exact set of client_id URLs admitted. An
	// empty set with enabled=true admits any document that validates.
	allowed map[string]struct{}
}

// NewClientMetadataPolicy parses IDENTITY_CLIENT_METADATA_DOCUMENTS.
//
//	""            — refused. No metadata client may sign anyone in.
//	"any"         — any client_id URL whose document validates.
//	"<url>,<url>" — only these exact client_id URLs.
//
// Unset means off for the same reason an unset client registry registers
// nobody: an allowlist that defaults to admitting is not an allowlist. Every
// malformed input fails startup rather than being dropped.
func NewClientMetadataPolicy(raw string) (ClientMetadataPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ClientMetadataPolicy{}, nil
	}
	if strings.EqualFold(raw, "any") {
		return ClientMetadataPolicy{enabled: true}, nil
	}
	policy := ClientMetadataPolicy{enabled: true, allowed: map[string]struct{}{}}
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		clientID, err := CanonicalMetadataClientID(candidate)
		if err != nil {
			return ClientMetadataPolicy{}, fmt.Errorf(
				"client metadata documents: %q is not a usable client_id URL: %w", candidate, err)
		}
		policy.allowed[clientID] = struct{}{}
	}
	if len(policy.allowed) == 0 {
		return ClientMetadataPolicy{}, errors.New(
			"client metadata documents: the declaration names no client_id URL; leave it unset to refuse metadata clients")
	}
	return policy, nil
}

// Enabled reports whether any metadata client may sign a person in.
func (p ClientMetadataPolicy) Enabled() bool { return p.enabled }

// Admits reports whether this exact client_id URL is within the policy.
func (p ClientMetadataPolicy) Admits(clientID string) error {
	if !p.enabled {
		return ErrClientMetadataDisabled
	}
	if len(p.allowed) == 0 {
		return nil
	}
	if _, ok := p.allowed[clientID]; !ok {
		return ErrClientMetadataNotAllowed
	}
	return nil
}

// IsMetadataClientID reports whether a presented client_id is a URL rather than
// a registry slug. The two namespaces cannot collide: ValidClientID admits only
// lowercase letters, digits, `-` and `_`, none of which can spell `https://`.
func IsMetadataClientID(clientID string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(clientID)), "https://")
}

// CanonicalMetadataClientID validates a metadata client_id and returns the
// exact string the document must echo. No normalisation beyond trimming
// surrounding space: the document's own client_id is compared for equality, and
// a host the host lower-cased would not match a document that did not.
func CanonicalMetadataClientID(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || len(candidate) > 2048 {
		return "", ErrClientMetadataClientID
	}
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", ErrClientMetadataClientID
	}
	// https only, with no exception for loopback. A client_id is a public
	// identity the host fetches over the internet; a loopback client_id would
	// name a document only this process can read, and would be an SSRF target
	// dressed as an identity.
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", ErrClientMetadataClientID
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
		return "", ErrClientMetadataClientID
	}
	if parsed.EscapedPath() == "" || parsed.EscapedPath() == "/" {
		return "", ErrClientMetadataClientID
	}
	return candidate, nil
}

// MetadataDocumentFetcher retrieves one client's metadata document. The
// production implementation is the bounded, SSRF-guarded HTTP fetch below; the
// seam exists so the authorization flow can be exercised — here and in the
// accounts service's own story tests — without a network, and so a composition
// that resolves documents some other way has somewhere to say so.
//
// freshness is how long the answer may be cached, which for HTTP comes from the
// document's own Cache-Control. A fetcher that has no opinion returns zero and
// gets the floor.
type MetadataDocumentFetcher interface {
	Fetch(ctx context.Context, clientID string) (document []byte, freshness time.Duration, err error)
}

// ClientMetadataResolver fetches, validates and caches metadata documents.
// Construct with NewClientMetadataResolver; the zero value resolves nothing.
type ClientMetadataResolver struct {
	policy  ClientMetadataPolicy
	fetcher MetadataDocumentFetcher
	now     func() time.Time

	mu     sync.Mutex
	cached map[string]cachedMetadataClient
}

// StaticMetadataDocuments answers from a fixed set of documents, keyed by
// client_id. The documents still go through every validation rule, so a test
// using it is testing the real admission decision and only skipping the
// transport.
type StaticMetadataDocuments map[string]string

// Fetch implements MetadataDocumentFetcher.
func (s StaticMetadataDocuments) Fetch(_ context.Context, clientID string) ([]byte, time.Duration, error) {
	document, ok := s[clientID]
	if !ok {
		return nil, 0, ErrClientMetadataUnreachable
	}
	return []byte(document), 0, nil
}

type cachedMetadataClient struct {
	client    RegisteredClient
	err       error
	expiresAt time.Time
}

// metadataDialGuard refuses to open a connection to a non-public address. It
// runs after DNS resolution with the concrete IP being dialed, so it blocks the
// address the connection would actually reach, closing the DNS-rebinding gap a
// hostname-only check leaves open. Package-level var so tests can point a
// resolver at a loopback server; production always uses the guard.
var metadataDialGuard = func(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrClientMetadataUnreachable
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ErrClientMetadataUnreachable
	}
	return nil
}

// NewClientMetadataResolver builds a resolver for one policy, fetching
// documents over HTTPS with the production bounds and SSRF guard.
func NewClientMetadataResolver(policy ClientMetadataPolicy) *ClientMetadataResolver {
	return NewClientMetadataResolverWith(policy, newHTTPMetadataFetcher())
}

// NewClientMetadataResolverWith builds a resolver over a given fetcher.
func NewClientMetadataResolverWith(
	policy ClientMetadataPolicy,
	fetcher MetadataDocumentFetcher,
) *ClientMetadataResolver {
	return &ClientMetadataResolver{
		policy:  policy,
		fetcher: fetcher,
		now:     time.Now,
		cached:  map[string]cachedMetadataClient{},
	}
}

// httpMetadataFetcher is the production fetch: https only, bounded in size and
// time, no redirect off the document's own origin, and guarded at the dial
// against every non-public address.
type httpMetadataFetcher struct {
	client *http.Client
}

func newHTTPMetadataFetcher() *httpMetadataFetcher {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: clientMetadataFetchTimeout,
			Control: metadataDialGuard,
		}).DialContext,
		TLSHandshakeTimeout:   clientMetadataFetchTimeout,
		ResponseHeaderTimeout: clientMetadataFetchTimeout,
		DisableKeepAlives:     false,
		MaxIdleConnsPerHost:   2,
	}
	return &httpMetadataFetcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   clientMetadataFetchTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= clientMetadataMaxRedirects {
					return ErrClientMetadataRedirected
				}
				origin := via[0].URL
				if !strings.EqualFold(req.URL.Scheme, "https") ||
					!strings.EqualFold(req.URL.Host, origin.Host) {
					return ErrClientMetadataRedirected
				}
				return nil
			},
		},
	}
}

// Enabled reports whether this resolver would admit any metadata client.
func (r *ClientMetadataResolver) Enabled() bool {
	return r != nil && r.policy.Enabled()
}

// Resolve turns a metadata client_id into a client of the registry for one
// flow. The returned error is one of the named refusals above; callers serving
// an unauthenticated caller must collapse it to ErrClientAuthorizationRejected.
func (r *ClientMetadataResolver) Resolve(ctx context.Context, clientID string) (RegisteredClient, error) {
	if r == nil {
		return RegisteredClient{}, ErrClientMetadataDisabled
	}
	canonical, err := CanonicalMetadataClientID(clientID)
	if err != nil {
		return RegisteredClient{}, err
	}
	if err := r.policy.Admits(canonical); err != nil {
		return RegisteredClient{}, err
	}
	if client, err, ok := r.fromCache(canonical); ok {
		return client, err
	}
	document, freshness, err := r.fetcher.Fetch(ctx, canonical)
	var client RegisteredClient
	if err == nil {
		client, err = ClientFromMetadataDocument(canonical, document)
	}
	r.remember(canonical, client, err, freshness)
	return client, err
}

func (r *ClientMetadataResolver) fromCache(clientID string) (RegisteredClient, error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cached[clientID]
	if !ok || !r.now().Before(entry.expiresAt) {
		return RegisteredClient{}, nil, false
	}
	return entry.client, entry.err, true
}

func (r *ClientMetadataResolver) remember(clientID string, client RegisteredClient, err error, ttl time.Duration) {
	switch {
	case err != nil:
		ttl = clientMetadataFailureTTL
	case ttl < clientMetadataMinTTL:
		ttl = clientMetadataMinTTL
	case ttl > clientMetadataMaxTTL:
		ttl = clientMetadataMaxTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The cache is keyed by an attacker-choosable URL, so it is bounded. At the
	// bound the whole map is dropped rather than evicted one entry at a time: a
	// resolver that needs more than this many distinct clients in a TTL window
	// is not a deployment this cache is sized for, and refetching is correct
	// where guessing which entry to keep is not.
	const maxCachedMetadataClients = 512
	if len(r.cached) >= maxCachedMetadataClients {
		r.cached = map[string]cachedMetadataClient{}
	}
	r.cached[clientID] = cachedMetadataClient{
		client:    client,
		err:       err,
		expiresAt: r.now().Add(ttl),
	}
}

// Fetch implements MetadataDocumentFetcher.
func (f *httpMetadataFetcher) Fetch(ctx context.Context, clientID string) ([]byte, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, clientMetadataFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, 0, ErrClientMetadataUnreachable
	}
	request.Header.Set("Accept", "application/json")
	response, err := f.client.Do(request)
	if err != nil {
		if errors.Is(err, ErrClientMetadataRedirected) {
			return nil, 0, ErrClientMetadataRedirected
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrClientMetadataUnreachable, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, response.Body); _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%w: status %d", ErrClientMetadataUnreachable, response.StatusCode)
	}
	// One byte over the bound is read deliberately, so a document AT the bound
	// is accepted and one above it is refused rather than silently truncated
	// into a document that parses to something smaller than what was published.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxClientMetadataBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrClientMetadataUnreachable, err)
	}
	if len(body) > maxClientMetadataBytes {
		return nil, 0, ErrClientMetadataTooLarge
	}
	return body, metadataCacheTTL(response.Header.Get("Cache-Control")), nil
}

// ClientFromMetadataDocument validates one document body against the URL it was
// served from and projects it onto a registry client. Exported so the rules can
// be tested against a real published document without a network.
func ClientFromMetadataDocument(clientID string, body []byte) (RegisteredClient, error) {
	// Unmarshal, not a streaming Decode: Decode reads the first JSON value and
	// ignores whatever follows it, so a document with a second object appended
	// would be accepted as though only the first were published. Unknown MEMBERS
	// are still ignored, as the draft requires — it is trailing CONTENT that is
	// refused.
	var document ClientMetadataDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return RegisteredClient{}, fmt.Errorf("%w: %v", ErrClientMetadataMalformed, err)
	}
	// The document's own client_id must be the URL it was served from. This is
	// the load-bearing check of the whole mechanism: without it, anyone who can
	// publish a JSON file can claim to be any client.
	if document.ClientID != clientID {
		return RegisteredClient{}, ErrClientMetadataIdentityMismatch
	}
	if document.TokenEndpointAuthMethod != "none" {
		return RegisteredClient{}, ErrClientMetadataAuthMethod
	}
	if err := requireDeclared(document.GrantTypes, "authorization_code"); err != nil {
		return RegisteredClient{}, err
	}
	if err := requireDeclared(document.ResponseTypes, "code"); err != nil {
		return RegisteredClient{}, err
	}
	if len(document.RedirectURIs) == 0 {
		return RegisteredClient{}, ErrClientMetadataRedirectURIs
	}
	client := RegisteredClient{ClientID: clientID, Metadata: true}
	for _, candidate := range document.RedirectURIs {
		// Every redirect URI goes through the rule operator-declared clients
		// already obey: absolute, https or loopback http, no userinfo, no
		// fragment. One that fails is dropped rather than failing the whole
		// document — a client may publish a URI for a scheme this host does not
		// serve (a custom app scheme, say) and still have a usable loopback one.
		redirectURI, err := canonicalClientRedirectURI(candidate)
		if err != nil {
			continue
		}
		client.RedirectURIs = append(client.RedirectURIs, redirectURI)
	}
	if len(client.RedirectURIs) == 0 {
		return RegisteredClient{}, ErrClientMetadataRedirectURIs
	}
	client.Name = strings.TrimSpace(document.ClientName)
	if client.Name == "" || len(client.Name) > 128 {
		// A name is presentational and comes from an unvetted document, so a
		// missing or oversized one falls back to the origin the document was
		// served from — which is the one fact about this client the host
		// verified. It is never truncated: a truncated name is a name chosen by
		// whoever published it.
		client.Name = metadataClientOrigin(clientID)
	}
	// Origins are deliberately NOT derived from the document. A browser origin
	// is what the gateway's CORS pass binds a token to, and a client that
	// published its own would be choosing its own CORS grant. A metadata client
	// is a native or server-side client talking to the host directly; a browser
	// client still needs an operator-declared row.
	client.URI = metadataClientOrigin(clientID)
	if uri := strings.TrimSpace(document.ClientURI); uri != "" {
		if parsed, err := url.Parse(uri); err == nil && parsed.IsAbs() &&
			strings.EqualFold(parsed.Scheme, "https") && parsed.Host != "" {
			client.URI = strings.ToLower(parsed.Scheme) + "://" + parsed.Host
		}
	}
	return client, nil
}

// requireDeclared accepts an absent list (RFC 7591's defaults are exactly the
// values this host serves) and otherwise requires the value to be present.
func requireDeclared(declared []string, required string) error {
	if len(declared) == 0 {
		return nil
	}
	for _, value := range declared {
		if value == required {
			return nil
		}
	}
	return ErrClientMetadataGrantTypes
}

func metadataClientOrigin(clientID string) string {
	parsed, err := url.Parse(clientID)
	if err != nil || parsed.Host == "" {
		return clientID
	}
	return strings.ToLower(parsed.Scheme) + "://" + parsed.Host
}

// metadataCacheTTL reads the document's own freshness, bounded. A document that
// says nothing, or says no-store, gets the floor rather than zero: the point of
// the floor is that a document's cache policy cannot turn this host into a
// request-rate amplifier against its own publisher.
func metadataCacheTTL(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		name, value, found := strings.Cut(strings.TrimSpace(directive), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "max-age") {
			continue
		}
		seconds, err := time.ParseDuration(strings.TrimSpace(value) + "s")
		if err != nil {
			continue
		}
		switch {
		case seconds < clientMetadataMinTTL:
			return clientMetadataMinTTL
		case seconds > clientMetadataMaxTTL:
			return clientMetadataMaxTTL
		default:
			return seconds
		}
	}
	return clientMetadataMinTTL
}
