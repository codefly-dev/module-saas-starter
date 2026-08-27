package business

import (
	"context"
	"regexp"
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
// access credential and persisting only its envelope reference. actorID is the
// authenticated principal, recorded on the audit event.
func (s *Service) AddGitHubSource(ctx context.Context, actorID string, input AddGitHubSourceInput) (*Datasource, error) {
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
		if err := s.store.CreateDatasource(ctx, ds); err != nil {
			return err
		}
		// Transactional audit: the source is not created without its
		// compliance record. A failed audit write aborts the insert.
		return s.emitTx(ctx, actorID, "user", EventDatasourceSourceAdded, "datasource", ds.ID, orgID,
			map[string]any{"repo": ds.Repo, "collection": ds.Collection})
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
// actorID is the authenticated principal, recorded on the audit event.
func (s *Service) SyncDatasource(ctx context.Context, actorID, orgID, id string) (*Datasource, error) {
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
		if ds == nil {
			// No own-org row matched; leave out nil and skip the audit
			// event — nothing changed to record.
			return nil
		}
		out = ds
		// Transactional audit: the sync-request flag and its compliance
		// record commit together.
		return s.emitTx(ctx, actorID, "user", EventDatasourceSyncRequested, "datasource", id, orgID, nil)
	}); err != nil {
		return nil, w.Wrapf(err, "request datasource sync")
	}
	if out == nil {
		return nil, w.NewError("datasource not found")
	}
	return out, nil
}

// githubRepoSegment is GitHub's allowed character set for an owner or repository
// name. Constraining both segments here keeps a stored repo from smuggling URL
// metacharacters (spaces, '?', '#', additional path separators) into the GitHub
// API URLs the connector builds downstream.
var githubRepoSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validGitHubRepo accepts a single "owner/name" slug where each segment is a
// valid GitHub identifier. It rejects empty segments, out-of-charset input, the
// "."/".." traversal names GitHub itself forbids, and over-length segments
// (GitHub caps owners at 39 and repositories at 100 characters).
func validGitHubRepo(repo string) bool {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return false
	}
	if !githubRepoSegment.MatchString(owner) || !githubRepoSegment.MatchString(name) {
		return false
	}
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return false
	}
	if len(owner) > 39 || len(name) > 100 {
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
