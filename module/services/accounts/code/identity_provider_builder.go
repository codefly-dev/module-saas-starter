package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/auth/oidc"
	workosauth "accounts/pkg/auth/workos"
	"accounts/pkg/business"
	"accounts/pkg/infra"
)

// oidcProviderStackBuilder builds a live per-org OIDC stack from a stored
// configuration row. It lives in the composition root because the oidc/workos
// adapter packages depend on pkg/business; a builder using them cannot live
// inside that package. Header-JWT providers are configurable but their runtime
// stack is a separate follow-up, so building one is refused explicitly rather
// than stubbed.
type oidcProviderStackBuilder struct {
	cipher business.SecretCipher
}

func (b oidcProviderStackBuilder) Build(ctx context.Context, provider *business.OrgIdentityProvider) (business.ProviderStack, error) {
	if provider.Kind != business.IdentityProviderKindOIDC {
		return business.ProviderStack{}, fmt.Errorf("%w: %q", business.ErrProviderKindUnsupported, provider.Kind)
	}
	if strings.TrimSpace(provider.Issuer) == "" {
		return business.ProviderStack{}, fmt.Errorf("org %s oidc provider requires an issuer", provider.OrgID)
	}
	if strings.TrimSpace(provider.ClientID) == "" || strings.TrimSpace(provider.ClientSecretRef) == "" {
		return business.ProviderStack{}, fmt.Errorf("org %s oidc provider requires a client id and client secret", provider.OrgID)
	}

	clientSecret, err := b.cipher.DecryptSecret(ctx, business.OrgIdentityProviderSecretPurpose(provider.OrgID), provider.ClientSecretRef)
	if err != nil {
		return business.ProviderStack{}, fmt.Errorf("decrypt org %s client secret: %w", provider.OrgID, err)
	}

	// Start from any pinned endpoints; discovery fills only the gaps, and a
	// discovery outage fails closed rather than guessing.
	expectedIssuer := provider.Issuer
	jwksURL := provider.JWKSURL
	tokenURL := provider.TokenURL
	if jwksURL == "" || tokenURL == "" {
		discoverCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		discovered, err := oidc.Discover(discoverCtx, provider.Issuer, nil)
		if err != nil {
			return business.ProviderStack{}, fmt.Errorf("discover org %s provider metadata: %w", provider.OrgID, err)
		}
		expectedIssuer = discovered.Issuer
		if jwksURL == "" {
			jwksURL = discovered.JWKSURI
		}
		if tokenURL == "" {
			tokenURL = discovered.TokenEndpoint
		}
	}
	if strings.TrimSpace(tokenURL) == "" {
		return business.ProviderStack{}, fmt.Errorf("org %s provider published no token endpoint", provider.OrgID)
	}

	name := business.OrgProviderName(provider.OrgID)
	cfg := oidc.Config{
		ProviderName: name,
		Issuer:       expectedIssuer,
		JWKSURL:      jwksURL,
		Audience:     provider.Audience,
		ClientID:     provider.ClientID,
		OrgClaim:     claimOrDefault(provider.ClaimMap, "org", "organization_id"),
		EmailClaim:   claimOrDefault(provider.ClaimMap, "email", ""),
	}
	validator, err := oidc.New(cfg)
	if err != nil {
		return business.ProviderStack{}, fmt.Errorf("initialize org %s validator: %w", provider.OrgID, err)
	}
	exchanger, err := workosauth.NewExchanger(workosauth.Config{
		TokenURL:     tokenURL,
		ClientID:     provider.ClientID,
		ClientSecret: clientSecret,
		Validator:    validator,
	})
	if err != nil {
		return business.ProviderStack{}, fmt.Errorf("initialize org %s exchanger: %w", provider.OrgID, err)
	}
	return business.ProviderStack{Name: name, Validator: validator, Exchanger: exchanger}, nil
}

func claimOrDefault(claimMap map[string]string, key, fallback string) string {
	if claimMap != nil {
		if v := strings.TrimSpace(claimMap[key]); v != "" {
			return v
		}
	}
	return fallback
}

// newIdentityProviderRegistry wires the registry with a store-backed lookup
// that returns only an org's active provider (so pending/disabled rows fall
// through to the global default) and the OIDC builder. globalValidator and
// globalExchanger are the process-wide default stack, used by orgs without an
// active provider.
func newIdentityProviderRegistry(
	store *infra.PostgresStore,
	cipher business.SecretCipher,
	globalName string,
	globalValidator auth.TokenValidator,
	globalExchanger business.CodeExchanger,
) *business.IdentityProviderRegistry {
	lookup := func(ctx context.Context, orgID string) (*business.OrgIdentityProvider, error) {
		var provider *business.OrgIdentityProvider
		if err := store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
			p, err := store.GetOrgIdentityProvider(ctx, orgID)
			provider = p
			return err
		}); err != nil {
			return nil, err
		}
		if provider == nil || provider.Status != business.IdentityProviderStatusActive {
			return nil, nil
		}
		return provider, nil
	}
	global := business.ProviderStack{Name: globalName, Validator: globalValidator, Exchanger: globalExchanger}
	return business.NewIdentityProviderRegistry(lookup, oidcProviderStackBuilder{cipher: cipher}, global)
}
