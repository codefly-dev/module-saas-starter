// Package devvalidator implements auth.TokenValidator for local development.
//
// It replaces the legacy X-Dev-Role / X-Dev-User-ID header bypass with a
// proper validator that reads seeded identities from the dev-admin fixture
// file. `fixture_token` is the development login credential; `provider_id`
// remains the stable external identity subject. Keeping these separate
// prevents a fixture shortcut from changing the identity trusted clients
// resolve.
//
// Only wired when AUTH_PROVIDER=dev or Codefly explicitly selects a fixture.
// Production startup leaves this validator disconnected from Authenticate.
package devvalidator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"accounts/pkg/auth"
)

// fixtureFile is the shape of module/fixtures/dev-admin.yaml.
// Only the users section is consumed here; orgs/teams live in the seed
// runner, not the token validator.
type fixtureFile struct {
	Users []fixtureUser `yaml:"users"`
}

type fixtureUser struct {
	Email        string `yaml:"email"`
	Name         string `yaml:"name"`
	Role         string `yaml:"role"` // optional: "super_admin" | "admin" | "member"
	Provider     string `yaml:"provider"`
	ProviderID   string `yaml:"provider_id"`
	FixtureToken string `yaml:"fixture_token"`
	MFAVerified  bool   `yaml:"mfa_verified"`
}

// Validator is a dev-only TokenValidator backed by a fixture YAML file.
//
// Thread-safe: the seed is parsed once at construction and held read-only.
type Validator struct {
	mu          sync.RWMutex
	seeds       map[string]*auth.Claims // keyed by fixture_token (provider_id fallback)
	mfaVerified map[string]bool         // explicit development-only AAL2 attestations
}

// New constructs a Validator from a fixture path. If path is empty it
// defaults to the dev-admin fixture under the module root.
func New(path string) (*Validator, error) {
	if path == "" {
		var err error
		path, err = defaultPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("devvalidator: cannot read fixture %q: %w", path, err)
	}
	var f fixtureFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("devvalidator: cannot parse fixture: %w", err)
	}

	v := &Validator{
		seeds:       make(map[string]*auth.Claims, len(f.Users)),
		mfaVerified: make(map[string]bool, len(f.Users)),
	}
	for i := range f.Users {
		u := f.Users[i]
		if u.ProviderID == "" || u.Email == "" {
			continue
		}
		provider := u.Provider
		if provider == "" {
			provider = "dev"
		}
		token := u.FixtureToken
		if token == "" {
			token = u.ProviderID // backwards-compatible fixtures
		}
		v.seeds[token] = &auth.Claims{
			Provider:  provider,
			Subject:   u.ProviderID,
			Email:     u.Email,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
		v.mfaVerified[token] = u.MFAVerified
	}
	if len(v.seeds) == 0 {
		return nil, fmt.Errorf("devvalidator: fixture %q contained no usable users", path)
	}
	return v, nil
}

// FixtureMFAWasVerified reports whether the allowlisted development identity
// represents a login ceremony that already completed a second factor. The
// business auth path uses this only after the same token passed Validate.
// Production never wires this validator, so fixture assurance cannot cross
// the development authentication boundary.
func (v *Validator) FixtureMFAWasVerified(token string) bool {
	v.mu.RLock()
	verified := v.mfaVerified[token]
	v.mu.RUnlock()
	return verified
}

// Validate implements auth.TokenValidator. The token is the fixture-only login
// credential; returned Claims carry the distinct provider_id subject. Unknown
// tokens return ErrUnknownIdentity.
//
// No signature check happens — this is a dev-only path. Production uses the
// WorkOS validator.
func (v *Validator) Validate(ctx context.Context, token string) (*auth.Claims, error) {
	if token == "" {
		return nil, auth.ErrMissingSubject
	}
	v.mu.RLock()
	claims, ok := v.seeds[token]
	v.mu.RUnlock()
	if !ok {
		return nil, auth.ErrUnknownIdentity
	}
	// Return a copy so callers can't mutate the cached seed.
	out := *claims
	return &out, nil
}

// defaultPath resolves the dev-admin fixture path relative to the running
// api service. The working directory when codefly starts the accounts service
// is services/accounts/code; the fixture lives at ../../../fixtures/dev-admin.yaml.
func defaultPath() (string, error) {
	if env := os.Getenv("DEV_FIXTURE_PATH"); env != "" {
		return env, nil
	}
	return filepath.Join("..", "..", "..", "fixtures", "dev-admin.yaml"), nil
}

// Compile-time assertion.
var _ auth.TokenValidator = (*Validator)(nil)
