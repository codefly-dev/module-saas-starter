package business

import (
	"context"
	"testing"

	"accounts/pkg/auth"
)

// OAuth initiation and the callback code exchange must authorize a redirect the
// same way. They did not: BeginOAuth bound the redirect to the verified frontend
// origin, while the exchange consulted only the static allowlist. Under the
// frontend-entry architecture that allowlist is empty by design — the gateway
// authenticates the browser origin instead — so every real login was rejected
// after the provider round-trip with "provider or redirect rejected".
//
// These cases cover validateOAuthRequest directly, which is the only honest
// place for them: the pipeline tier runs on the fixture identity provider, where
// no OAuth policy is constructed at all, so an end-to-end assertion there would
// pass or fail for reasons unrelated to this logic.
func TestValidateOAuthRequestBindsRedirectToVerifiedOrigin(t *testing.T) {
	const (
		provider = "workos"
		origin   = "http://localhost:21931"
	)

	policy, err := auth.NewOAuthRequestPolicy(provider, nil)
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	service := &Service{}
	service.SetOAuthRequestPolicy(policy)

	trusted, err := auth.WithVerifiedPublicOrigin(context.Background(), origin)
	if err != nil {
		t.Fatalf("record verified origin: %v", err)
	}

	t.Run("accepts the verified origin's own callback", func(t *testing.T) {
		if err := service.validateOAuthRequest(trusted, provider, origin+"/auth/callback"); err != nil {
			t.Fatalf("verified-origin callback rejected: %v", err)
		}
	})

	t.Run("rejects another host's callback", func(t *testing.T) {
		if err := service.validateOAuthRequest(trusted, provider, "https://attacker.example.com/auth/callback"); err == nil {
			t.Fatal("callback on a foreign origin was accepted")
		}
	})

	t.Run("rejects an alternate path on the verified origin", func(t *testing.T) {
		if err := service.validateOAuthRequest(trusted, provider, origin+"/auth/callback/../admin"); err == nil {
			t.Fatal("alternate path on the verified origin was accepted")
		}
	})

	t.Run("rejects a provider mismatch", func(t *testing.T) {
		if err := service.validateOAuthRequest(trusted, "google", origin+"/auth/callback"); err == nil {
			t.Fatal("mismatched provider was accepted")
		}
	})

	t.Run("without a verified origin falls back to the empty allowlist", func(t *testing.T) {
		// A direct request that never crossed the trusted frontend boundary has
		// no verified origin, and the static allowlist is empty unless an
		// operator configures IDENTITY_ALLOWED_REDIRECT_URIS. It must stay closed.
		if err := service.validateOAuthRequest(context.Background(), provider, origin+"/auth/callback"); err == nil {
			t.Fatal("untrusted request was accepted against an empty allowlist")
		}
	})

	t.Run("without a verified origin honors a configured allowlist", func(t *testing.T) {
		configured, err := auth.NewOAuthRequestPolicy(provider, []string{origin + "/auth/callback"})
		if err != nil {
			t.Fatalf("build configured policy: %v", err)
		}
		operatorService := &Service{}
		operatorService.SetOAuthRequestPolicy(configured)

		if err := operatorService.validateOAuthRequest(context.Background(), provider, origin+"/auth/callback"); err != nil {
			t.Fatalf("configured redirect rejected: %v", err)
		}
	})

	t.Run("rejects everything when no policy is configured", func(t *testing.T) {
		if err := (&Service{}).validateOAuthRequest(trusted, provider, origin+"/auth/callback"); err == nil {
			t.Fatal("request accepted without an OAuth policy")
		}
	})
}
