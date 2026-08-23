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
// reference. A brand-new provider, or a change to any trust-affecting field
// (kind, issuer, client id, endpoints, audience, claim mapping, or a rotated
// secret), lands in 'pending' so a stub/demo configuration never writes
// status='active' and a security-relevant change forces re-verification via a
// deliberate ActivateOrgIdentityProvider call (MFA-gated in the admin surface).
// A metadata-only edit (display name, allowlists, vanity host) preserves the
// current status, so touching a label does not silently drop an active org out
// of SSO. When no client secret is supplied, the stored envelope is preserved
// rather than wiped.
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
	// The runtime stack binds an org's OIDC tokens to the org through the `aud`
	// claim, so a provider without an audience would accept any token the issuer
	// signs. Reject it here rather than at first login (see oidcProviderStackBuilder).
	if kind == IdentityProviderKindOIDC && strings.TrimSpace(input.Audience) == "" {
		return nil, w.NewError("oidc provider requires an audience")
	}

	secretSupplied := strings.TrimSpace(input.ClientSecret) != ""
	newSecretRef := ""
	if secretSupplied {
		if s.identityCipher == nil {
			return nil, w.NewError("identity provider secret cipher is not configured")
		}
		envelope, err := s.identityCipher.EncryptSecret(ctx, OrgIdentityProviderSecretPurpose(orgID), input.ClientSecret)
		if err != nil {
			return nil, w.Wrapf(err, "encrypt client secret")
		}
		newSecretRef = envelope
	}

	provider := &OrgIdentityProvider{
		OrgID:               orgID,
		Kind:                kind,
		DisplayName:         displayName,
		Issuer:              strings.TrimSpace(input.Issuer),
		ClientID:            strings.TrimSpace(input.ClientID),
		JWKSURL:             strings.TrimSpace(input.JWKSURL),
		TokenURL:            strings.TrimSpace(input.TokenURL),
		Audience:            strings.TrimSpace(input.Audience),
		ClaimMap:            input.ClaimMap,
		AllowedEmailDomains: normalizeDomains(input.AllowedEmailDomains),
		AllowedGroups:       input.AllowedGroups,
		VanityHost:          normalizeHost(input.VanityHost),
	}

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		existing, err := s.store.GetOrgIdentityProvider(ctx, orgID)
		if err != nil {
			return err
		}
		provider.ID = NewIDString()
		provider.ClientSecretRef = newSecretRef
		provider.Status = IdentityProviderStatusPending
		if existing != nil {
			provider.ID = existing.ID
			if !secretSupplied {
				provider.ClientSecretRef = existing.ClientSecretRef
			}
			// A metadata-only edit keeps the current status; a trust-affecting
			// change forces re-verification back to pending.
			if !orgProviderTrustChanged(existing, provider, secretSupplied) {
				provider.Status = existing.Status
			}
		}
		return s.store.UpsertOrgIdentityProvider(ctx, provider)
	}); err != nil {
		return nil, w.Wrapf(err, "persist org identity provider")
	}
	s.invalidateOrgProvider(orgID)
	return provider, nil
}

// orgProviderTrustChanged reports whether a reconfiguration changed a field that
// bears on the trust relationship with the IdP — the identity of the provider,
// its endpoints, or how its claims are interpreted — or rotated the secret.
// Metadata fields (display name, allowlists, vanity host) are deliberately
// excluded: they are the org admin's routing/policy knobs, not IdP trust.
func orgProviderTrustChanged(existing, candidate *OrgIdentityProvider, secretRotated bool) bool {
	return secretRotated ||
		existing.Kind != candidate.Kind ||
		existing.Issuer != candidate.Issuer ||
		existing.ClientID != candidate.ClientID ||
		existing.JWKSURL != candidate.JWKSURL ||
		existing.TokenURL != candidate.TokenURL ||
		existing.Audience != candidate.Audience ||
		!claimMapEqual(existing.ClaimMap, candidate.ClaimMap)
}

func claimMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
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
