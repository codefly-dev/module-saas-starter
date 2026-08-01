package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discovery is the subset of the OpenID Provider Metadata this service needs.
//
// Every OpenID-compliant provider publishes this document, so the issuer, key
// set and endpoints are facts the provider states about itself rather than
// constants compiled into this repository. Hardcoding them per provider is how
// an environment ends up rejecting every token with "issuer mismatch" *after* a
// successful code exchange: the value was wrong in Go, and nothing could
// correct it from configuration.
//
// See https://openid.net/specs/openid-connect-discovery-1_0.html#ProviderMetadata
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

// DiscoveryURL derives the well-known metadata URL from an issuer, per the
// discovery spec: the path is appended to the issuer, preserving any path the
// issuer already carries. A URL that already points at the well-known document
// is returned unchanged, so operators may configure either form.
func DiscoveryURL(issuer string) (string, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return "", fmt.Errorf("oidc: issuer is required to discover provider metadata")
	}
	parsed, err := url.Parse(issuer)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("oidc: issuer must be an absolute URL: %q", issuer)
	}
	if !strings.EqualFold(parsed.Scheme, "https") && !isLoopbackDiscoveryHost(parsed.Hostname()) {
		return "", fmt.Errorf("oidc: issuer must use HTTPS: %q", issuer)
	}
	if strings.Contains(parsed.Path, "/.well-known/") {
		return parsed.String(), nil
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/.well-known/openid-configuration"
	return parsed.String(), nil
}

// Discover fetches and validates provider metadata.
//
// The returned issuer is authoritative: the spec requires it to match the
// document's location, and it is what the provider will place in `iss`.
func Discover(ctx context.Context, issuer string, client *http.Client) (Discovery, error) {
	endpoint, err := DiscoveryURL(issuer)
	if err != nil {
		return Discovery{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Discovery{}, fmt.Errorf("oidc: build discovery request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return Discovery{}, fmt.Errorf("oidc: fetch provider metadata from %s: %w", endpoint, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Discovery{}, fmt.Errorf("oidc: read provider metadata: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf(
			"oidc: provider metadata at %s returned http %d",
			endpoint, response.StatusCode,
		)
	}

	var discovered Discovery
	if err := json.Unmarshal(body, &discovered); err != nil {
		return Discovery{}, fmt.Errorf("oidc: parse provider metadata: %w", err)
	}
	if strings.TrimSpace(discovered.Issuer) == "" {
		return Discovery{}, fmt.Errorf("oidc: provider metadata at %s declares no issuer", endpoint)
	}
	if strings.TrimSpace(discovered.JWKSURI) == "" {
		return Discovery{}, fmt.Errorf("oidc: provider metadata at %s declares no jwks_uri", endpoint)
	}
	// OpenID Connect Discovery §4.3: the returned `issuer` MUST be identical to
	// the issuer identifier used to fetch this document. Skipping this check
	// would let a document served under one issuer declare itself the authority
	// for another and be trusted as the expected `iss` for every token.
	if want := issuerIdentifier(issuer); want != issuerIdentifier(discovered.Issuer) {
		return Discovery{}, fmt.Errorf(
			"oidc: provider metadata at %s declares issuer %q, expected %q",
			endpoint, discovered.Issuer, want)
	}
	return discovered, nil
}

// issuerIdentifier normalizes an issuer for the §4.3 equality check: it drops a
// well-known metadata suffix (so an operator may configure either the issuer or
// the document URL) and a single trailing slash (which providers include
// inconsistently).
func issuerIdentifier(issuer string) string {
	issuer = strings.TrimSpace(issuer)
	if i := strings.Index(issuer, "/.well-known/"); i >= 0 {
		issuer = issuer[:i]
	}
	return strings.TrimRight(issuer, "/")
}

func isLoopbackDiscoveryHost(host string) bool {
	return strings.EqualFold(host, "localhost") ||
		host == "127.0.0.1" ||
		host == "::1"
}
