package business

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/codefly-dev/core/wool"

	"accounts/pkg/githubconnector"
)

// ConnectorProviderGitHub is the only connector provider today. It gates the
// provider column's CHECK constraint (migration 103).
const ConnectorProviderGitHub = "github"

const connectorSecretPurposePrefix = "github-connector:"

// ErrSourceCredentialNotFound is returned when no credential is stored for a
// source. The inbound-webhook receiver relies on this to reject a delivery for
// an unknown source rather than admitting an unsigned one.
var ErrSourceCredentialNotFound = errors.New("source credential not found")

// ConnectorSecretPurpose binds a source's credential envelope to that source so
// ciphertext cannot be replayed across sources. The source id (a globally
// unique UUID) is the binding, matching the issue's github-connector:<source_id>
// purpose.
func ConnectorSecretPurpose(sourceID string) string {
	return connectorSecretPurposePrefix + sourceID
}

// SourceCredential is the plaintext secret set a connector needs for one
// datasource: the GitHub App credential it mints installation tokens from
// (for repo-contents pulls), and the HMAC secret GitHub signs inbound webhook
// deliveries with. Both are optional so a source can carry either or both;
// they are persisted together as a single purpose-bound envelope.
type SourceCredential struct {
	GitHubApp     *githubconnector.AppCredential `json:"github_app,omitempty"`
	WebhookSecret string                         `json:"webhook_secret,omitempty"`
}

func (c SourceCredential) validate() error {
	if c.GitHubApp == nil && strings.TrimSpace(c.WebhookSecret) == "" {
		return errors.New("source credential must carry a github app credential, a webhook secret, or both")
	}
	if c.GitHubApp != nil {
		return c.GitHubApp.Validate()
	}
	return nil
}

// ConnectorCredential is the stored row: the encrypted envelope plus its source
// binding and provider. The plaintext SourceCredential never leaves the
// business layer.
type ConnectorCredential struct {
	ID              string
	OrgID           string
	SourceID        string
	Provider        string
	SecretEncrypted string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PutGitHubSourceCredential encrypts a source's credential set and upserts it
// (one row per source). The secret is stored only as a purpose-bound envelope
// reference, never plaintext.
func (s *Service) PutGitHubSourceCredential(ctx context.Context, orgID, sourceID string, cred SourceCredential) error {
	w := wool.Get(ctx).In("PutGitHubSourceCredential")
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return w.NewError("org id is required")
	}
	sourceID, err := normalizeSourceID(sourceID)
	if err != nil {
		return w.Wrap(err)
	}
	if err := cred.validate(); err != nil {
		return w.Wrap(err)
	}
	if s.connectorCipher == nil {
		return w.NewError("connector secret cipher is not configured")
	}

	plaintext, err := json.Marshal(cred)
	if err != nil {
		return w.Wrapf(err, "encode source credential")
	}
	envelope, err := s.connectorCipher.EncryptSecret(ctx, ConnectorSecretPurpose(sourceID), string(plaintext))
	if err != nil {
		return w.Wrapf(err, "encrypt source credential")
	}

	record := &ConnectorCredential{
		ID:              NewIDString(),
		OrgID:           orgID,
		SourceID:        sourceID,
		Provider:        ConnectorProviderGitHub,
		SecretEncrypted: envelope,
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.UpsertConnectorCredential(ctx, record)
	}); err != nil {
		return w.Wrapf(err, "persist source credential")
	}
	return nil
}

// GetGitHubSourceCredential loads and decrypts a source's credential set for the
// caller's org. It returns ErrSourceCredentialNotFound when the org has no
// credential for the source.
func (s *Service) GetGitHubSourceCredential(ctx context.Context, orgID, sourceID string) (SourceCredential, error) {
	w := wool.Get(ctx).In("GetGitHubSourceCredential")
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return SourceCredential{}, w.NewError("org id is required")
	}
	sourceID, err := normalizeSourceID(sourceID)
	if err != nil {
		return SourceCredential{}, w.Wrap(err)
	}

	var envelope string
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		record, err := s.store.GetConnectorCredential(ctx, sourceID)
		if err != nil {
			return err
		}
		if record == nil {
			return ErrSourceCredentialNotFound
		}
		envelope = record.SecretEncrypted
		return nil
	}); err != nil {
		return SourceCredential{}, err
	}
	return s.decryptSourceCredential(ctx, sourceID, envelope)
}

// DeleteSourceCredential removes a source's credential for the caller's org.
// Deleting an absent credential is a no-op.
func (s *Service) DeleteSourceCredential(ctx context.Context, orgID, sourceID string) error {
	w := wool.Get(ctx).In("DeleteSourceCredential")
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return w.NewError("org id is required")
	}
	sourceID, err := normalizeSourceID(sourceID)
	if err != nil {
		return w.Wrap(err)
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.DeleteConnectorCredential(ctx, sourceID)
	}); err != nil {
		return w.Wrapf(err, "delete source credential")
	}
	return nil
}

// SourceSigningSecret returns the HMAC secret GitHub signs a source's webhook
// deliveries with. Inbound-webhook receipt is unauthenticated (GitHub, not a
// tenant session, makes the call), so the lookup is a cross-tenant control-plane
// read keyed by the globally unique source id — the same pre-auth path the
// identity-provider discovery uses. It returns ErrSourceCredentialNotFound when
// no source, or no signing secret for that source, is configured. This is the
// resolver an unauthenticated webhook receiver calls with a client-controlled
// path segment, so a source id that isn't even a UUID cannot name a stored row
// and is reported as a clean miss (ErrSourceCredentialNotFound) rather than a
// distinct error — otherwise every probe of an invalid path would be logged by
// the caller as a resolver failure.
func (s *Service) SourceSigningSecret(ctx context.Context, sourceID string) (string, error) {
	w := wool.Get(ctx).In("SourceSigningSecret")
	sourceID, err := normalizeSourceID(sourceID)
	if err != nil {
		return "", ErrSourceCredentialNotFound
	}
	if s.connectorCipher == nil {
		return "", w.NewError("connector secret cipher is not configured")
	}

	var envelope string
	if err := s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		record, err := s.store.GetConnectorCredential(ctx, sourceID)
		if err != nil {
			return err
		}
		if record == nil {
			return ErrSourceCredentialNotFound
		}
		envelope = record.SecretEncrypted
		return nil
	}); err != nil {
		return "", err
	}

	cred, err := s.decryptSourceCredential(ctx, sourceID, envelope)
	if err != nil {
		return "", err
	}
	if cred.WebhookSecret == "" {
		return "", ErrSourceCredentialNotFound
	}
	return cred.WebhookSecret, nil
}

// FetchGitHubSourceContents authenticates with the source's stored GitHub App
// credential and pulls the repository contents at path. This is the entry point
// SyncSource and webhook re-fetch call.
func (s *Service) FetchGitHubSourceContents(ctx context.Context, orgID, sourceID, owner, repo, path, ref string) (*githubconnector.RepoContent, error) {
	w := wool.Get(ctx).In("FetchGitHubSourceContents")
	if s.githubConnector == nil {
		return nil, w.NewError("github connector is not configured")
	}
	cred, err := s.GetGitHubSourceCredential(ctx, orgID, sourceID)
	if err != nil {
		return nil, err
	}
	if cred.GitHubApp == nil {
		return nil, w.NewError("source has no github app credential")
	}
	return s.githubConnector.FetchRepoContents(ctx, *cred.GitHubApp, owner, repo, path, ref)
}

// decryptSourceCredential decrypts an envelope outside the store transaction so
// the Vault round-trip does not hold a pooled database connection.
func (s *Service) decryptSourceCredential(ctx context.Context, sourceID, envelope string) (SourceCredential, error) {
	w := wool.Get(ctx).In("decryptSourceCredential")
	plaintext, err := s.connectorCipher.DecryptSecret(ctx, ConnectorSecretPurpose(sourceID), envelope)
	if err != nil {
		return SourceCredential{}, w.Wrapf(err, "decrypt source credential")
	}
	var cred SourceCredential
	if err := json.Unmarshal([]byte(plaintext), &cred); err != nil {
		return SourceCredential{}, w.Wrapf(err, "decode source credential")
	}
	return cred, nil
}

func normalizeSourceID(sourceID string) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return "", errors.New("source id is required")
	}
	if _, err := uuid.Parse(sourceID); err != nil {
		return "", errors.New("source id must be a uuid")
	}
	return sourceID, nil
}
