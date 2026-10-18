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

// Validate checks a request that carried no verified public origin against the
// operator's static redirect allowlist.
//
// Every refusal is ErrInvalidOAuthRequest, which is all a public handler maps on,
// so the caller never learns which part of the allowlist was wrong. The wrapped
// text names the check that failed and the values compared — provider names and
// redirect URIs are not secrets — for the refuser's log alone.
func (p *OAuthRequestPolicy) Validate(provider, redirectURI string) error {
	if err := p.validateProvider(provider); err != nil {
		return err
	}
	if _, ok := p.redirectURIs[redirectURI]; !ok {
		return fmt.Errorf("%w: redirect_uri %q is not in the static redirect allowlist (identity/IDENTITY_ALLOWED_REDIRECT_URIS, %d entries)",
			ErrInvalidOAuthRequest, redirectURI, len(p.redirectURIs))
	}
	return nil
}

// ValidateForPublicOrigin binds OAuth initiation to the authenticated frontend
// request origin. The callback path is server-owned; the browser may not select
// an alternate path, host, or port. Refusals wrap ErrInvalidOAuthRequest as
// Validate's do.
func (p *OAuthRequestPolicy) ValidateForPublicOrigin(provider, redirectURI, publicOrigin string) error {
	if err := p.validateProvider(provider); err != nil {
		return err
	}
	origin, err := CanonicalPublicOrigin(publicOrigin)
	if err != nil {
		return fmt.Errorf("%w: verified public origin %q is not canonical: %v", ErrInvalidOAuthRequest, publicOrigin, err)
	}
	if want := origin + "/auth/callback"; redirectURI != want {
		return fmt.Errorf("%w: redirect_uri %q is not the verified public origin's callback %q",
			ErrInvalidOAuthRequest, redirectURI, want)
	}
	return nil
}

func (p *OAuthRequestPolicy) validateProvider(provider string) error {
	if p == nil {
		return fmt.Errorf("%w: no OAuth request policy is configured", ErrInvalidOAuthRequest)
	}
	if provider != p.provider {
		return fmt.Errorf("%w: provider %q is not the configured provider %q", ErrInvalidOAuthRequest, provider, p.provider)
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
