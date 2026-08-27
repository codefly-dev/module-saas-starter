package business

import (
	"context"
	"strings"
	"time"

	"github.com/codefly-dev/core/wool"
)

// Datasource kinds and sync states. The connection side tracks whether a sync
// has been requested; the documents ingest worker owns actual progress.
const (
	DatasourceKindGitHub = "github"

	DatasourceSyncStatusIdle    = "idle"
	DatasourceSyncStatusPending = "pending"
)

const datasourceSecretPurposePrefix = "datasource:"

// Datasource is one organization's external source connection. CredentialSecretRef
// holds a SecretCipher envelope (a reference into the secret provider), never a
// plaintext access token.
type Datasource struct {
	ID                  string
	OrgID               string
	Kind                string
	Repo                string
	Paths               []string
	Collection          string
	CredentialSecretRef string
	SyncStatus          string
	LastSyncRequestedAt time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// DatasourceSecretPurpose binds a credential envelope to one source so
// ciphertext cannot be replayed across rows. The row id is the binding because
// a source is created once and its id is stable.
func DatasourceSecretPurpose(id string) string {
	return datasourceSecretPurposePrefix + id
}

// AddGitHubSourceInput is the caller-supplied configuration for a GitHub source.
// Credential is plaintext; it is encrypted through the SecretCipher and only its
// envelope reference is persisted.
type AddGitHubSourceInput struct {
	OrgID      string
	Repo       string
	Paths      []string
	Collection string
	Credential string
}

// AddGitHubSource records a GitHub source for the org, encrypting the supplied
// access credential and persisting only its envelope reference.
func (s *Service) AddGitHubSource(ctx context.Context, input AddGitHubSourceInput) (*Datasource, error) {
	w := wool.Get(ctx).In("AddGitHubSource")

	orgID := strings.TrimSpace(input.OrgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	repo := strings.TrimSpace(input.Repo)
	if !validGitHubRepo(repo) {
		return nil, w.NewError("repo must be in owner/name form")
	}
	collection := strings.TrimSpace(input.Collection)
	if collection == "" {
		return nil, w.NewError("collection is required")
	}
	if strings.TrimSpace(input.Credential) == "" {
		return nil, w.NewError("credential is required")
	}
	if s.datasourceCipher == nil {
		return nil, w.NewError("datasource secret cipher is not configured")
	}

	id := NewIDString()
	envelope, err := s.datasourceCipher.EncryptSecret(ctx, DatasourceSecretPurpose(id), input.Credential)
	if err != nil {
		return nil, w.Wrapf(err, "encrypt credential")
	}

	ds := &Datasource{
		ID:                  id,
		OrgID:               orgID,
		Kind:                DatasourceKindGitHub,
		Repo:                repo,
		Paths:               normalizePaths(input.Paths),
		Collection:          collection,
		CredentialSecretRef: envelope,
		SyncStatus:          DatasourceSyncStatusIdle,
	}
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		return s.store.CreateDatasource(ctx, ds)
	}); err != nil {
		return nil, w.Wrapf(err, "persist datasource")
	}
	return ds, nil
}

// ListDatasources returns the org's configured sources.
func (s *Service) ListDatasources(ctx context.Context, orgID string) ([]*Datasource, error) {
	w := wool.Get(ctx).In("ListDatasources")

	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	var out []*Datasource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		list, err := s.store.ListDatasources(ctx, orgID)
		if err != nil {
			return err
		}
		out = list
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "list datasources")
	}
	return out, nil
}

// SyncDatasource marks a source for ingestion. RLS confines the update to the
// caller's org, so a source id belonging to another org resolves to not-found.
func (s *Service) SyncDatasource(ctx context.Context, orgID, id string) (*Datasource, error) {
	w := wool.Get(ctx).In("SyncDatasource")

	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return nil, w.NewError("org id is required")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, w.NewError("id is required")
	}
	var out *Datasource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		ds, err := s.store.MarkDatasourceSyncRequested(ctx, id)
		if err != nil {
			return err
		}
		out = ds
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "request datasource sync")
	}
	if out == nil {
		return nil, w.NewError("datasource not found")
	}
	return out, nil
}

// validGitHubRepo accepts a single "owner/name" slug: exactly one slash and no
// whitespace or empty segment.
func validGitHubRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false
	}
	if strings.ContainsAny(repo, " \t\n") || strings.Contains(name, "/") {
		return false
	}
	return true
}

// normalizePaths trims each path and drops empties, returning an empty (non-nil)
// slice when nothing survives so the NOT NULL array column keeps its default.
func normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
