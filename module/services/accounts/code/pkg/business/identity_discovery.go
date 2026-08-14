package business

import (
	"context"
	"strings"
)

// ProviderDiscovery is the result of a pre-auth provider lookup. Its shape is
// constant: an unknown or non-allowlisted email/host yields the zero value
// (Available=false, no name), identical to any other miss, so the response
// never enumerates orgs or reveals which domains are configured.
type ProviderDiscovery struct {
	Available       bool
	OrgProviderName string
	Kind            string
}

// DiscoverProviderByEmail maps an email's domain to an org's active provider so
// the sign-in page can route the user to their tenant's IdP. It resolves only
// for domains explicitly allowlisted on an active provider; every other input
// returns the constant "not available" result.
func (s *Service) DiscoverProviderByEmail(ctx context.Context, email string) (ProviderDiscovery, error) {
	domain := emailDomain(email)
	if domain == "" {
		return ProviderDiscovery{}, nil
	}
	provider, err := s.store.ResolveOrgProviderByEmailDomain(ctx, domain)
	if err != nil {
		return ProviderDiscovery{}, err
	}
	return discoveryResult(provider), nil
}

// DiscoverProviderByHost maps an optional per-org vanity host to its active
// provider. Same constant-shape guarantee as email discovery.
func (s *Service) DiscoverProviderByHost(ctx context.Context, host string) (ProviderDiscovery, error) {
	host = normalizeHost(host)
	if host == "" {
		return ProviderDiscovery{}, nil
	}
	provider, err := s.store.ResolveOrgProviderByHost(ctx, host)
	if err != nil {
		return ProviderDiscovery{}, err
	}
	return discoveryResult(provider), nil
}

func discoveryResult(p *OrgIdentityProvider) ProviderDiscovery {
	if p == nil || p.Status != IdentityProviderStatusActive {
		return ProviderDiscovery{}
	}
	return ProviderDiscovery{
		Available:       true,
		OrgProviderName: OrgProviderName(p.OrgID),
		Kind:            p.Kind,
	}
}

// emailDomain extracts the lowercased domain from an email address, or returns
// "" when the input is not a single, well-formed address.
func emailDomain(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.Count(email, "@") != 1 {
		return ""
	}
	at := strings.IndexByte(email, '@')
	if at == 0 || at == len(email)-1 {
		return ""
	}
	domain := email[at+1:]
	if strings.ContainsAny(domain, " \t") || !strings.Contains(domain, ".") {
		return ""
	}
	return domain
}
