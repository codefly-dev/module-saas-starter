package business

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
)

// Identity provider kinds and lifecycle statuses. A per-org provider is
// configured 'pending', promoted to 'active' by a deliberate activation step,
// and 'disabled' when an admin pauses it (the row is preserved so re-enabling
// is cheap).
const (
	IdentityProviderKindOIDC      = "oidc"
	IdentityProviderKindHeaderJWT = "header-jwt"

	IdentityProviderStatusPending  = "pending"
	IdentityProviderStatusActive   = "active"
	IdentityProviderStatusDisabled = "disabled"
)

const orgIdentityProviderSecretPurposePrefix = "org-idp:"

// OrgIdentityProvider is one organization's identity provider configuration.
// ClientSecretRef holds a SecretCipher envelope (a reference into the secret
// provider), never a plaintext secret.
type OrgIdentityProvider struct {
	ID                  string
	OrgID               string
	Kind                string
	DisplayName         string
	Issuer              string
	ClientID            string
	ClientSecretRef     string
	JWKSURL             string
	TokenURL            string
	Audience            string
	ClaimMap            map[string]string
	AllowedEmailDomains []string
	AllowedGroups       []string
	VanityHost          string
	Status              string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// OrgProviderName is the stable provider identifier recorded in
// user_identities.provider for an org's dedicated IdP. Keying it by org id
// guarantees identities minted from different tenants' providers never collide,
// even when the underlying subjects are identical.
func OrgProviderName(orgID string) string {
	return "oidc:" + orgID
}

// OrgIdentityProviderSecretPurpose binds a client-secret envelope to one org so
// ciphertext cannot be replayed across rows. The org id (not the row id) is the
// binding because org_id is the table's stable identity — one provider per org.
func OrgIdentityProviderSecretPurpose(orgID string) string {
	return orgIdentityProviderSecretPurposePrefix + orgID
}

// OrgIdentityProviderInput is the caller-supplied configuration for an org's
// provider. ClientSecret is plaintext; it is encrypted through the SecretCipher
// and only its envelope reference is persisted.
type OrgIdentityProviderInput struct {
	OrgID               string
	Kind                string
	DisplayName         string
	Issuer              string
	ClientID            string
	ClientSecret        string
	JWKSURL             string
	TokenURL            string
	Audience            string
	ClaimMap            map[string]string
	AllowedEmailDomains []string
	AllowedGroups       []string
	VanityHost          string
}

// ConfigureOrgIdentityProvider creates or replaces an org's provider
// configuration. The client secret is encrypted and stored only as an envelope
// reference. A new or re-configured provider always lands in 'pending' — a
// stub/demo configuration never writes status='active'; a deliberate
// ActivateOrgIdentityProvider call (MFA-gated in the admin surface) promotes it.
func (s *Service) ConfigureOrgIdentityProvider(ctx context.Context, input OrgIdentityProviderInput) (*OrgIdentityProvider, error) {
	w := wool.Get(ctx).In("ConfigureOrgIdentityProvider")

	orgID := strings.TrimSpace(input.OrgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind != IdentityProviderKindOIDC && kind != IdentityProviderKindHeaderJWT {
		return nil, w.NewError("unsupported identity provider kind %q", kind)
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		return nil, w.NewError("display name is required")
	}
	if kind == IdentityProviderKindOIDC && strings.TrimSpace(input.Issuer) == "" {
		return nil, w.NewError("oidc provider requires an issuer")
	}

	secretRef := ""
	if strings.TrimSpace(input.ClientSecret) != "" {
		if s.identityCipher == nil {
			return nil, w.NewError("identity provider secret cipher is not configured")
		}
		envelope, err := s.identityCipher.EncryptSecret(ctx, OrgIdentityProviderSecretPurpose(orgID), input.ClientSecret)
		if err != nil {
			return nil, w.Wrapf(err, "encrypt client secret")
		}
		secretRef = envelope
	}

	provider := &OrgIdentityProvider{
		ID:                  NewIDString(),
		OrgID:               orgID,
		Kind:                kind,
		DisplayName:         displayName,
		Issuer:              strings.TrimSpace(input.Issuer),
		ClientID:            strings.TrimSpace(input.ClientID),
		ClientSecretRef:     secretRef,
		JWKSURL:             strings.TrimSpace(input.JWKSURL),
		TokenURL:            strings.TrimSpace(input.TokenURL),
		Audience:            strings.TrimSpace(input.Audience),
		ClaimMap:            input.ClaimMap,
		AllowedEmailDomains: normalizeDomains(input.AllowedEmailDomains),
		AllowedGroups:       input.AllowedGroups,
		VanityHost:          normalizeHost(input.VanityHost),
		Status:              IdentityProviderStatusPending,
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.UpsertOrgIdentityProvider(ctx, provider)
	}); err != nil {
		return nil, w.Wrapf(err, "persist org identity provider")
	}
	s.invalidateOrgProvider(orgID)
	return provider, nil
}

// ActivateOrgIdentityProvider promotes a configured provider to 'active' and
// invalidates the registry cache so the change takes effect without a restart.
func (s *Service) ActivateOrgIdentityProvider(ctx context.Context, orgID string) error {
	return s.setOrgIdentityProviderStatus(ctx, orgID, IdentityProviderStatusActive)
}

// DisableOrgIdentityProvider marks a provider 'disabled' and invalidates the
// registry cache. Sessions already minted stay valid until they expire; only
// new sign-ins are affected.
func (s *Service) DisableOrgIdentityProvider(ctx context.Context, orgID string) error {
	return s.setOrgIdentityProviderStatus(ctx, orgID, IdentityProviderStatusDisabled)
}

func (s *Service) setOrgIdentityProviderStatus(ctx context.Context, orgID, status string) error {
	w := wool.Get(ctx).In("setOrgIdentityProviderStatus")
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return w.NewError("org id is required")
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.SetOrgIdentityProviderStatus(ctx, orgID, status)
	}); err != nil {
		return w.Wrapf(err, "update org identity provider status")
	}
	s.invalidateOrgProvider(orgID)
	return nil
}

// GetOrgIdentityProvider returns the org's configured provider, or nil when the
// org uses the global default provider.
func (s *Service) GetOrgIdentityProvider(ctx context.Context, orgID string) (*OrgIdentityProvider, error) {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, fmt.Errorf("org id is required")
	}
	var provider *OrgIdentityProvider
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		p, err := s.store.GetOrgIdentityProvider(ctx, orgID)
		provider = p
		return err
	}); err != nil {
		return nil, err
	}
	return provider, nil
}

func (s *Service) invalidateOrgProvider(orgID string) {
	if s.identityRegistry != nil {
		s.identityRegistry.Invalidate(orgID)
	}
}

func normalizeDomains(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}
	out := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(host))
}
