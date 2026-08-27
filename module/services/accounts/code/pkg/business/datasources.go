package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
)

// Datasource connectors and connection lifecycle statuses. A source is created
// 'pending' (no usable credential yet), promoted to 'active' once the connector
// stores a credential, 'disabled' when an admin pauses it, and 'error' when the
// last connection or sync attempt failed.
const (
	DatasourceConnectorGitHub = "github"

	DatasourceSourceStatusPending  = "pending"
	DatasourceSourceStatusActive   = "active"
	DatasourceSourceStatusDisabled = "disabled"
	DatasourceSourceStatusError    = "error"
)

// GitHubSourceConfig selects the repository content a GitHub source ingests.
type GitHubSourceConfig struct {
	Repository string   `json:"repository"`
	Branch     string   `json:"branch,omitempty"`
	Paths      []string `json:"paths,omitempty"`
}

// DatasourceConfig is the connector-specific settings persisted as JSONB. Only
// the field matching the source's connector is populated, so the table stays
// connector-agnostic.
type DatasourceConfig struct {
	GitHub *GitHubSourceConfig `json:"github,omitempty"`
}

// DatasourceSource is one org-owned connection to an external system.
// CredentialSecretRef holds a SecretCipher envelope (a reference into the
// secret provider) populated by the connector, never a plaintext credential.
type DatasourceSource struct {
	ID                  string
	OrgID               string
	Connector           string
	DisplayName         string
	TargetCollection    string
	Config              DatasourceConfig
	CredentialSecretRef string
	Status              string
	LastSyncRequestedAt *time.Time
	LastSyncedAt        *time.Time
	LastSyncError       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CreateDatasourceSource persists a new org-owned source. The source starts
// 'pending'; the connector activates it once a credential is stored.
func (s *Service) CreateDatasourceSource(ctx context.Context, actorID string, source *DatasourceSource) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("CreateDatasourceSource")

	if source.Connector != DatasourceConnectorGitHub {
		return nil, w.NewError("unsupported datasource connector %q", source.Connector)
	}
	if source.Config.GitHub == nil || source.Config.GitHub.Repository == "" {
		return nil, w.NewError("github source requires a repository")
	}

	source.ID = NewIDString()
	source.Status = DatasourceSourceStatusPending

	var stored *DatasourceSource
	if err := s.store.WithOrgTx(ctx, source.OrgID, func(ctx context.Context) error {
		if err := s.store.CreateDatasourceSource(ctx, source); err != nil {
			return err
		}
		var err error
		stored, err = s.store.GetDatasourceSource(ctx, source.ID)
		return err
	}); err != nil {
		return nil, w.Wrapf(err, "cannot create datasource source")
	}

	s.emit(ctx, actorID, "user", "datasource.source.created", "datasource_source", source.ID, source.OrgID)

	return stored, nil
}

// GetDatasourceSource returns one source owned by the caller's org. RLS scopes
// the lookup, so a cross-tenant id resolves to not-found rather than leaking
// another org's row.
func (s *Service) GetDatasourceSource(ctx context.Context, orgID, id string) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("GetDatasourceSource")

	var source *DatasourceSource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		got, err := s.store.GetDatasourceSource(ctx, id)
		if err != nil {
			return err
		}
		source = got
		return nil
	}); err != nil {
		return nil, w.Wrapf(err, "cannot load datasource source")
	}
	if source == nil {
		return nil, w.NewError("datasource source not found")
	}
	return source, nil
}

// ListDatasourceSources returns an org's sources, newest first.
func (s *Service) ListDatasourceSources(ctx context.Context, orgID string) ([]*DatasourceSource, error) {
	var out []*DatasourceSource
	err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		sources, err := s.store.ListDatasourceSources(ctx, orgID)
		out = sources
		return err
	})
	return out, err
}

// DeleteDatasourceSource removes a source owned by the caller's org. RLS makes
// the delete safe: a cross-tenant id affects zero rows and reports not-found.
func (s *Service) DeleteDatasourceSource(ctx context.Context, actorID, orgID, id string) error {
	w := wool.Get(ctx).In("DeleteDatasourceSource")

	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		source, err := s.store.GetDatasourceSource(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot load datasource source")
		}
		if source == nil {
			return w.NewError("datasource source not found")
		}
		return s.store.DeleteDatasourceSource(ctx, id)
	}); err != nil {
		return err
	}

	s.emit(ctx, actorID, "user", "datasource.source.deleted", "datasource_source", id, orgID)

	return nil
}

// RequestDatasourceSync records a sync request against a source. It stamps
// last_sync_requested_at; the connector worker compares that against
// last_synced_at to perform the actual pull. The updated source is returned.
func (s *Service) RequestDatasourceSync(ctx context.Context, actorID, orgID, id string) (*DatasourceSource, error) {
	w := wool.Get(ctx).In("RequestDatasourceSync")

	var source *DatasourceSource
	if err := s.store.WithOrgTx(ctx, orgID, func(ctx context.Context) error {
		updated, err := s.store.MarkDatasourceSourceSyncRequested(ctx, id)
		if err != nil {
			return w.Wrapf(err, "cannot record sync request")
		}
		if updated == nil {
			return w.NewError("datasource source not found")
		}
		source = updated
		return nil
	}); err != nil {
		return nil, err
	}

	s.emit(ctx, actorID, "user", "datasource.source.sync_requested", "datasource_source", id, orgID)

	return source, nil
}
