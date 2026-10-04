package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrClientAuthorizationRejected covers every reason a client's authorization
// request is refused: an unknown client id, a redirect URI that client did not
// register, an unsupported challenge method. Callers must not turn those apart,
// because the login page serves this refusal to an unauthenticated browser and
// a distinguishable answer would enumerate the registry.
var ErrClientAuthorizationRejected = errors.New("client authorization rejected")

// RegisteredClient is one public client the host will sign a person in for: an
// add-in, a CLI, a mobile app, an MCP client. It is a public client — it ships
// to the person using it and can keep no secret — so it authenticates with PKCE
// and a rotating refresh token.
//
// RedirectURIs and Origins are independent: where the host may deliver an
// authorization code and where the client's browser context issues requests
// from are different facts, and deriving one from the other would silently
// widen whichever was not declared.
//
// A client reaches this type two ways. The operator declares it in
// IDENTITY_REGISTERED_CLIENTS, in which case Metadata is false and the
// deployment has vouched for it. Or the client presents a Client ID Metadata
// Document (client_metadata.go), in which case Metadata is true, nothing
// durable was written, and the person is asked to consent by name.
type RegisteredClient struct {
	ClientID     string
	Name         string
	RedirectURIs []string
	Origins      []string
	// URI is the client's own home page, shown on the consent screen beside its
	// name so the person can see where a client they did not install came from.
	// Empty for an operator-declared client, which the deployment vouched for.
	URI string
	// Metadata marks a client resolved from a Client ID Metadata Document
	// rather than from operator configuration.
	Metadata bool
}

// AllowsRedirect reports whether candidate is one of the URIs registered for
// this client. Matching is exact — never by prefix, suffix, or host — so no
// added path segment or appended query can widen it, with one bounded exception
// below for loopback redirects.
//
// The exception is RFC 8252 §7.3: a native client listens on an ephemeral
// loopback port it cannot know in advance, so a registration that names a
// loopback http URI WITHOUT a port matches that URI on any port. A registration
// that names a port still matches only that port — so declaring
// `http://localhost:3000/auth/callback` keeps meaning port 3000, and no
// existing registration is widened by this rule. Only a deliberately
// port-less declaration opts in.
func (c RegisteredClient) AllowsRedirect(candidate string) bool {
	for _, registered := range c.RedirectURIs {
		if registered == candidate {
			return true
		}
		if loopbackRedirectMatchesAnyPort(registered, candidate) {
			return true
		}
	}
	return false
}

// loopbackRedirectMatchesAnyPort implements the RFC 8252 §7.3 loopback rule for
// one registered/presented pair. It applies only when the registered URI is
// http on a loopback host and declares no port; everything else about the two
// URIs — scheme, host, path, query — must still be identical.
func loopbackRedirectMatchesAnyPort(registered, candidate string) bool {
	registeredURL, err := url.Parse(registered)
	if err != nil || !strings.EqualFold(registeredURL.Scheme, "http") ||
		registeredURL.Port() != "" || !isLoopbackHost(registeredURL.Hostname()) {
		return false
	}
	candidateURL, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil || candidateURL.User != nil || candidateURL.Fragment != "" {
		return false
	}
	if !strings.EqualFold(candidateURL.Scheme, "http") {
		return false
	}
	if !strings.EqualFold(candidateURL.Hostname(), registeredURL.Hostname()) {
		return false
	}
	// A port is optional on the presented URI too (the registered form is then
	// matched exactly by the caller above), but if present it must be a port and
	// nothing else — url.Parse accepts an empty or malformed port in some forms.
	if port := candidateURL.Port(); port != "" {
		for _, digit := range port {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return candidateURL.EscapedPath() == registeredURL.EscapedPath() &&
		candidateURL.RawQuery == registeredURL.RawQuery
}

// ClientRegistry is the resolved set of declared clients. It is built once at
// startup from operator configuration and never mutated, so lookups are
// lock-free.
type ClientRegistry struct {
	byID  map[string]RegisteredClient
	order []RegisteredClient
}

// Lookup resolves a client id. A registry that was never configured, or was
// configured empty, resolves nothing — which refuses every client rather than
// admitting any.
func (r *ClientRegistry) Lookup(clientID string) (RegisteredClient, bool) {
	if r == nil {
		return RegisteredClient{}, false
	}
	client, ok := r.byID[clientID]
	return client, ok
}

// All returns the declared clients in configuration order.
func (r *ClientRegistry) All() []RegisteredClient {
	if r == nil {
		return nil
	}
	return r.order
}

// clientDeclaration is the operator-facing JSON shape. Field names are the
// wire names a deployment writes, so they are spelled out rather than derived.
type clientDeclaration struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	RedirectURIs []string `json:"redirect_uris"`
	Origins      []string `json:"origins"`
}

// NewClientRegistry parses the declared clients from configuration. An empty or
// absent declaration is valid and yields an empty registry: a deployment with
// no first-party client beyond the host's own frontend registers none, and the
// flow then refuses every request rather than needing a separate off switch.
//
// Every other malformed input fails startup. A registry is an allowlist, and an
// allowlist that silently drops the entry it could not read is one that either
// locks out a working client or — worse, if the dropped part was a constraint —
// admits more than was written.
func NewClientRegistry(raw string) (*ClientRegistry, error) {
	registry := &ClientRegistry{byID: map[string]RegisteredClient{}}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return registry, nil
	}

	var declared []clientDeclaration
	if err := json.Unmarshal([]byte(raw), &declared); err != nil {
		return nil, fmt.Errorf("client registry: cannot parse declaration: %w", err)
	}
	for _, declaration := range declared {
		client, err := resolveClientDeclaration(declaration)
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.byID[client.ClientID]; duplicate {
			return nil, fmt.Errorf("client registry: %q is declared twice", client.ClientID)
		}
		registry.byID[client.ClientID] = client
		registry.order = append(registry.order, client)
	}
	return registry, nil
}

func resolveClientDeclaration(declaration clientDeclaration) (RegisteredClient, error) {
	clientID := strings.TrimSpace(declaration.ClientID)
	if !ValidClientID(clientID) {
		return RegisteredClient{}, fmt.Errorf("client registry: invalid client id %q", declaration.ClientID)
	}
	name := strings.TrimSpace(declaration.Name)
	if name == "" || len(name) > 128 {
		return RegisteredClient{}, fmt.Errorf("client registry: %s needs a name of up to 128 characters", clientID)
	}
	// Only public clients exist. Declaring any other kind is refused rather than
	// coerced, so a deployment that expected secret-based authentication is told
	// it is not getting it.
	if kind := strings.TrimSpace(declaration.Kind); kind != "" && kind != "public" {
		return RegisteredClient{}, fmt.Errorf("client registry: %s declares unsupported kind %q", clientID, kind)
	}
	if len(declaration.RedirectURIs) == 0 {
		return RegisteredClient{}, fmt.Errorf("client registry: %s declares no redirect URI", clientID)
	}

	client := RegisteredClient{ClientID: clientID, Name: name}
	for _, candidate := range declaration.RedirectURIs {
		redirectURI, err := canonicalClientRedirectURI(candidate)
		if err != nil {
			return RegisteredClient{}, fmt.Errorf("client registry: %s: %w", clientID, err)
		}
		client.RedirectURIs = append(client.RedirectURIs, redirectURI)
	}
	for _, candidate := range declaration.Origins {
		origin, err := CanonicalPublicOrigin(candidate)
		if err != nil {
			return RegisteredClient{}, fmt.Errorf("client registry: %s: %w", clientID, err)
		}
		// An origin is matched against the browser's Origin header by exact
		// string equality, so a pattern an operator meant as a wildcard would
		// register a host no browser can ever send: the client would simply have
		// no origin, and nothing would say why.
		if strings.ContainsAny(origin, "*?") {
			return RegisteredClient{}, fmt.Errorf("client registry: %s: origin must be exact, not a pattern: %q", clientID, candidate)
		}
		client.Origins = append(client.Origins, origin)
	}
	return client, nil
}

// ValidClientID reports whether a client id is well formed. The same shape is
// enforced on the declaration and on every request naming a client, so a value
// that could never be registered is refused before it reaches a lookup.
func ValidClientID(clientID string) bool {
	if len(clientID) < 2 || len(clientID) > 64 {
		return false
	}
	for index, character := range clientID {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
		case (character == '-' || character == '_') && index > 0:
		default:
			return false
		}
	}
	return true
}

// canonicalClientRedirectURI applies the rule NewOAuthRequestPolicy already
// applies to the host's own callbacks: an absolute HTTPS URI, or HTTP only for
// a loopback development host, carrying no credentials and no fragment. A
// fragment would be invisible to the server issuing the redirect, and userinfo
// changes who the browser believes it is talking to.
func canonicalClientRedirectURI(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("invalid redirect URI %q", candidate)
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery {
		return "", fmt.Errorf("redirect URI must not contain userinfo or fragment: %q", candidate)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return "", fmt.Errorf("non-loopback redirect URI must use HTTPS: %q", candidate)
		}
	default:
		return "", fmt.Errorf("redirect URI must use HTTP(S): %q", candidate)
	}
	return candidate, nil
}

// ChallengeMethodS256 is the only PKCE method this host accepts (RFC 7636).
// "plain" puts the verifier itself in the authorization request, which defeats
// the point, and OAuth 2.1 requires S256. It lives here rather than in business
// code because the published authorization-server metadata, the request
// validation, and the redemption check must all name the same constant.
const ChallengeMethodS256 = "S256"

// ValidCodeChallenge reports whether a presented S256 challenge is well formed:
// base64url, unpadded, in the length band a 32-byte digest produces. Checked at
// the authorization request so a challenge no verifier could ever match is
// refused before a code is minted against it.
func ValidCodeChallenge(challenge string) bool {
	if len(challenge) < 43 || len(challenge) > 128 {
		return false
	}
	for _, character := range challenge {
		switch {
		case character >= 'A' && character <= 'Z',
			character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.', character == '~':
		default:
			return false
		}
	}
	return true
}
