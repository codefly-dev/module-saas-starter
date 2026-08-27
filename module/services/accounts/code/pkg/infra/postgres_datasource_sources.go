package infra

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// datasourceSourceColumns is the shared projection; COALESCE keeps the optional
// text columns non-null so they scan into plain strings.
const datasourceSourceColumns = `
	id::text, org_id::text, connector, display_name, target_collection, config,
	COALESCE(credential_secret_ref, ''), status,
	last_sync_requested_at, last_synced_at, COALESCE(last_sync_error, ''),
	created_at, updated_at`

func scanDatasourceSource(row pgx.Row) (*business.DatasourceSource, error) {
	var src business.DatasourceSource
	var config []byte
	if err := row.Scan(
		&src.ID, &src.OrgID, &src.Connector, &src.DisplayName, &src.TargetCollection, &config,
		&src.CredentialSecretRef, &src.Status,
		&src.LastSyncRequestedAt, &src.LastSyncedAt, &src.LastSyncError,
		&src.CreatedAt, &src.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &src.Config); err != nil {
			return nil, err
		}
	}
	return &src, nil
}

// CreateDatasourceSource inserts a new source. Runs under the caller's
// WithOrgTx so the RLS WITH CHECK passes.
func (s *PostgresStore) CreateDatasourceSource(ctx context.Context, source *business.DatasourceSource) error {
	config, err := json.Marshal(source.Config)
	if err != nil {
		return err
	}
	_, err = s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasource_sources (
			id, org_id, connector, display_name, target_collection, config,
			credential_secret_ref, status)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8)`,
		source.ID, source.OrgID, source.Connector, source.DisplayName,
		source.TargetCollection, config, source.CredentialSecretRef, source.Status)
	return err
}

// GetDatasourceSource returns one source by id, or (nil, nil) when none exists.
// Runs under the caller's WithOrgTx.
func (s *PostgresStore) GetDatasourceSource(ctx context.Context, id string) (*business.DatasourceSource, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT `+datasourceSourceColumns+` FROM datasource_sources WHERE id = $1`, id)
	source, err := scanDatasourceSource(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return source, nil
}

// ListDatasourceSources returns an org's sources, newest first. Runs under the
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

// DeleteDatasourceSource removes a source by id. Runs under the caller's
// WithOrgTx; a cross-tenant id affects zero rows under RLS.
func (s *PostgresStore) DeleteDatasourceSource(ctx context.Context, id string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx,
		`DELETE FROM datasource_sources WHERE id = $1`, id)
	return err
}

// MarkDatasourceSourceSyncRequested stamps the most recent sync request and
// returns the updated row in one atomic statement, or (nil, nil) when no row
// matches. Runs under the caller's WithOrgTx.
func (s *PostgresStore) MarkDatasourceSourceSyncRequested(ctx context.Context, id string) (*business.DatasourceSource, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx, `
		UPDATE datasource_sources
		   SET last_sync_requested_at = NOW(), updated_at = NOW()
		 WHERE id = $1
		RETURNING `+datasourceSourceColumns, id)
	source, err := scanDatasourceSource(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return source, nil
}
