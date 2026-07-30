package auth

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// OAuthRequestPolicy restricts OAuth initiation and callback exchange to the
// single provider configured for this service and an exact set of redirect
// URIs registered by the operator. At the Codefly frontend entry, the request
// can instead be bound to the actual browser origin authenticated across the
// frontend-to-gateway boundary. Exact matching avoids suffix, wildcard,
// alternate-port, and path-confusion bypasses.
type OAuthRequestPolicy struct {
	provider     string
	redirectURIs map[string]struct{}
}

// NewOAuthRequestPolicy validates startup configuration. An empty static list
// is valid because trusted frontend requests use ValidateForPublicOrigin; direct
// requests remain denied by Validate. HTTP redirects are
// accepted only for loopback development hosts; non-loopback callbacks require
// HTTPS. Query strings are allowed when explicitly configured, but fragments
// and userinfo are never valid OAuth callback targets.
func NewOAuthRequestPolicy(provider string, redirectURIs []string) (*OAuthRequestPolicy, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" || strings.EqualFold(provider, "dev") {
		return nil, fmt.Errorf("oauth policy: production provider required")
	}

	policy := &OAuthRequestPolicy{
		provider:     provider,
		redirectURIs: make(map[string]struct{}, len(redirectURIs)),
	}
	for _, candidate := range redirectURIs {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || strings.EqualFold(candidate, "REPLACE_ME") {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil || !u.IsAbs() || u.Host == "" {
			return nil, fmt.Errorf("oauth policy: invalid redirect URI %q", candidate)
		}
		if u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("oauth policy: redirect URI must not contain userinfo or fragment: %q", candidate)
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
		case "http":
			if !isLoopbackHost(u.Hostname()) {
				return nil, fmt.Errorf("oauth policy: non-loopback redirect URI must use HTTPS: %q", candidate)
			}
		default:
			return nil, fmt.Errorf("oauth policy: redirect URI must use HTTP(S): %q", candidate)
		}
		policy.redirectURIs[candidate] = struct{}{}
	}
	return policy, nil
}

// Validate returns one generic sentinel for provider and redirect mismatches so
// public handlers do not reveal which part of the allowlist was wrong.
func (p *OAuthRequestPolicy) Validate(provider, redirectURI string) error {
	if p == nil || provider != p.provider {
		return ErrInvalidOAuthRequest
	}
	if _, ok := p.redirectURIs[redirectURI]; !ok {
		return ErrInvalidOAuthRequest
	}
	return nil
}

// ValidateForPublicOrigin binds OAuth initiation to the authenticated frontend
// request origin. The callback path is server-owned; the browser may not select
// an alternate path, host, or port.
func (p *OAuthRequestPolicy) ValidateForPublicOrigin(provider, redirectURI, publicOrigin string) error {
	if p == nil || provider != p.provider {
		return ErrInvalidOAuthRequest
	}
	origin, err := CanonicalPublicOrigin(publicOrigin)
	if err != nil || redirectURI != origin+"/auth/callback" {
		return ErrInvalidOAuthRequest
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
