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

// WorkOSConfig returns a Config preconfigured for WorkOS given a client id.
// The composition root supplies that id from the Codefly identity
// configuration; this package does not read process environment variables.
func WorkOSConfig(clientID string) Config {
	clientID = strings.TrimSpace(clientID)
	return Config{
		ProviderName: "workos",
		// AuthKit scopes the issuer to the application, as its own discovery
		// document reports:
		//   GET https://api.workos.com/user_management/<client id>/.well-known/openid-configuration
		//   → "issuer": "https://api.workos.com/user_management/<client id>"
		// The bare host never appears as `iss`, so expecting it rejects every
		// token with "token issuer mismatch" only after the code exchange has
		// already succeeded.
		Issuer:            fmt.Sprintf("https://api.workos.com/user_management/%s", clientID),
		JWKSURL:           fmt.Sprintf("https://api.workos.com/sso/jwks/%s", clientID),
		OrgClaim:          "org_id",
		ClientIDClaim:     "client_id",
		ClientID:          clientID,
		AllowMissingEmail: true,
	}
}

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
