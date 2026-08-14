package oidc

import (
	"fmt"
	"strings"
)

// Preset is a named bundle of provider-specific defaults (issuer URL,
// JWKS URL, claim names) that are filled in on top of a minimal caller
// config. Adding a new provider is typically ~10 lines here.
//
// Callers who need full control still use New(Config{...}) directly.

// WorkOS is not a preset: it is configured entirely from discovery plus the
// Codefly `identity` workspace configuration (see buildDiscoveredOIDCStack).
// Its endpoints are published at the provider's well-known document rather than
// compiled in, and its claim mapping (org_id, the client_id claim, and the
// email supplied from the token-exchange response) is expressed as IDENTITY_*
// configuration. A hardcoded preset would only reintroduce constants that drift
// from the provider.

// Auth0Config returns a Config preconfigured for an Auth0 tenant.
//
//	domain: "acme.auth0.com" (without scheme)
//	audience: your API identifier, e.g. "https://api.acme.com"
func Auth0Config(domain, audience string) Config {
	domain = strings.TrimSpace(domain)
	return Config{
		ProviderName: "auth0",
		Issuer:       fmt.Sprintf("https://%s/", domain),
		JWKSURL:      fmt.Sprintf("https://%s/.well-known/jwks.json", domain),
		Audience:     audience,
		// Auth0 puts org id under "org_id" rather than "organization_id".
		OrgClaim: "org_id",
	}
}

// ClerkConfig returns a Config preconfigured for a Clerk instance.
//
//	frontendAPI: the instance's frontend API domain, e.g.
//	             "clean-mastiff-42.clerk.accounts.dev" or "clerk.acme.com"
func ClerkConfig(frontendAPI string) Config {
	frontendAPI = strings.TrimSpace(frontendAPI)
	return Config{
		ProviderName: "clerk",
		Issuer:       fmt.Sprintf("https://%s", frontendAPI),
		JWKSURL:      fmt.Sprintf("https://%s/.well-known/jwks.json", frontendAPI),
		// Clerk doesn't expose an org id on the default session claim,
		// but exposes "org_id" when the "Organization" session template
		// is enabled.
		OrgClaim: "org_id",
	}
}

// OktaConfig returns a Config preconfigured for an Okta org or custom
// authorization server — one concrete instance of the generic
// IDENTITY_PROVIDER=oidc path, which discovers these same values from the
// provider's well-known document rather than compiling them in.
//
//	issuer: the full issuer URL, e.g. "https://acme.okta.com" (org
//	        authorization server) or "https://acme.okta.com/oauth2/aus1a2b3c"
//	        (a custom authorization server).
//	audience: the access token's `aud`; for ID-token logins this is the
//	          OAuth client id.
func OktaConfig(issuer, audience string) Config {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	return Config{
		ProviderName: "okta",
		Issuer:       issuer,
		JWKSURL:      issuer + "/v1/keys",
		Audience:     audience,
		// Okta exposes no organization id on the default token.
		OrgClaim: "",
	}
}

// GoogleConfig returns a Config preconfigured for Google Sign-In.
//
//	clientID: OAuth client id used as `aud`.
func GoogleConfig(clientID string) Config {
	return Config{
		ProviderName: "google",
		Issuer:       "https://accounts.google.com",
		JWKSURL:      "https://www.googleapis.com/oauth2/v3/certs",
		Audience:     clientID,
		// Google has no built-in org concept on the ID token.
		OrgClaim: "",
	}
}
