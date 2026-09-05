package infra

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// datasourceSourceColumns is the shared projection; COALESCE keeps the optional
// text columns non-null so they scan into plain strings (repo is null for
// providers that have no repository), while last_synced_at and config stay
// nullable.
const datasourceSourceColumns = `
	id::text, org_id::text, provider, COALESCE(repo, ''), paths, COALESCE(branch, ''),
	target_collection, credential_secret_ref, COALESCE(webhook_secret_ref, ''),
	status, last_synced_at, created_at, updated_at, config`

func scanDatasourceSource(row pgx.Row) (*business.DatasourceSource, error) {
	var d business.DatasourceSource
	var config []byte
	if err := row.Scan(
		&d.ID, &d.OrgID, &d.Provider, &d.Repo, &d.Paths, &d.Branch,
		&d.TargetCollection, &d.CredentialSecretRef, &d.WebhookSecretRef,
		&d.Status, &d.LastSyncedAt, &d.CreatedAt, &d.UpdatedAt, &config,
	); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		switch d.Provider {
		case business.DatasourceProviderAPI:
			var api business.APIDatasourceConfig
			if err := json.Unmarshal(config, &api); err != nil {
				return nil, err
			}
			d.API = &api
		case business.DatasourceProviderCrawler:
			var c business.CrawlerDatasourceConfig
			if err := json.Unmarshal(config, &c); err != nil {
				return nil, err
			}
			d.Crawler = &c
		case business.DatasourceProviderUpload:
			var u business.UploadDatasourceConfig
			if err := json.Unmarshal(config, &u); err != nil {
				return nil, err
			}
			d.Upload = &u
		}
	}
	return &d, nil
}

// InsertDatasourceSource writes a new connected Source. Runs under the caller's
// WithOrgTx.
func (s *PostgresStore) InsertDatasourceSource(ctx context.Context, source *business.DatasourceSource) error {
	paths := source.Paths
	if paths == nil {
		paths = []string{}
	}
	var payload any
	switch {
	case source.API != nil:
		payload = source.API
	case source.Crawler != nil:
		payload = source.Crawler
	case source.Upload != nil:
		payload = source.Upload
	}
	var config []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		config = encoded
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasource_sources (
			id, org_id, provider, repo, paths, branch, target_collection,
			credential_secret_ref, webhook_secret_ref, status, config)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, NULLIF($9, ''), $10, $11)`,
		source.ID, source.OrgID, source.Provider, source.Repo, paths, source.Branch,
		source.TargetCollection, source.CredentialSecretRef, source.WebhookSecretRef, source.Status, config,
	)
	return err
}

// ListDatasourceSources returns the org's Sources, newest first. Runs under the
// caller's WithOrgTx.
func (s *PostgresStore) ListDatasourceSources(ctx context.Context, orgID string) ([]*business.DatasourceSource, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+datasourceSourceColumns+`
		   FROM datasource_sources
		  WHERE org_id = $1
		  ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []*business.DatasourceSource
	for rows.Next() {
		source, err := scanDatasourceSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// GetDatasourceSource returns one org-scoped Source, or (nil, nil) when none
// matches. Runs under the caller's WithOrgTx.
func (s *PostgresStore) GetDatasourceSource(ctx context.Context, orgID, id string) (*business.DatasourceSource, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+datasourceSourceColumns+`
		   FROM datasource_sources WHERE org_id = $1 AND id = $2`, orgID, id)
	source, err := scanDatasourceSource(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return source, nil
}

// DeleteDatasourceSource removes an org-scoped Source. Runs under the caller's
// WithOrgTx.
func (s *PostgresStore) DeleteDatasourceSource(ctx context.Context, orgID, id string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM datasource_sources WHERE org_id = $1 AND id = $2`, orgID, id)
	return err
}

// SetDatasourceSourceSynced records the last successful sync time. Runs under
// the caller's WithOrgTx.
func (s *PostgresStore) SetDatasourceSourceSynced(ctx context.Context, orgID, id string, syncedAt time.Time) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_sources
		   SET last_synced_at = $3, updated_at = NOW()
		 WHERE org_id = $1 AND id = $2`, orgID, id, syncedAt)
	return err
}

// GetDatasourceSourceByID is the unauthenticated webhook-receipt lookup: no
// tenant context, so it opens its own control-plane transaction (BYPASSRLS is a
// database capability, not a client-settable GUC). Returns (nil, nil) on miss.
func (s *PostgresStore) GetDatasourceSourceByID(ctx context.Context, id string) (*business.DatasourceSource, error) {
	var source *business.DatasourceSource
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		row := s.getQueryExecutor(ctx).QueryRow(ctx,
			`SELECT `+datasourceSourceColumns+` FROM datasource_sources WHERE id = $1`, id)
		found, err := scanDatasourceSource(row)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		source = found
		return nil
	})
	if err != nil {
		return nil, err
	}
	return source, nil
}
