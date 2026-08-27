package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

// datasourceColumns is the shared projection. The credential envelope is never
// selected — reads must not surface it.
const datasourceColumns = `
	id::text, org_id::text, kind, repo, paths, collection,
	sync_status, last_sync_requested_at, created_at, updated_at`

func scanDatasource(row pgx.Row) (*business.Datasource, error) {
	var ds business.Datasource
	var lastSyncRequestedAt *time.Time
	if err := row.Scan(
		&ds.ID, &ds.OrgID, &ds.Kind, &ds.Repo, &ds.Paths, &ds.Collection,
		&ds.SyncStatus, &lastSyncRequestedAt, &ds.CreatedAt, &ds.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if lastSyncRequestedAt != nil {
		ds.LastSyncRequestedAt = *lastSyncRequestedAt
	}
	return &ds, nil
}

// CreateDatasource inserts a new source. One row per org+repo; a repeat repo
// conflicts. Runs under the caller's WithOrgTx.
func (s *PostgresStore) CreateDatasource(ctx context.Context, ds *business.Datasource) error {
	paths := ds.Paths
	if paths == nil {
		paths = []string{}
	}
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO datasources (
			id, org_id, kind, repo, paths, collection,
			credential_secret_ref, sync_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ds.ID, ds.OrgID, ds.Kind, ds.Repo, paths, ds.Collection,
		ds.CredentialSecretRef, ds.SyncStatus,
	)
	return err
}

// ListDatasources returns the org's sources, newest first. Runs under the
// caller's WithOrgTx.
func (s *PostgresStore) ListDatasources(ctx context.Context, orgID string) ([]*business.Datasource, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+datasourceColumns+` FROM datasources WHERE org_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*business.Datasource
	for rows.Next() {
		ds, err := scanDatasource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, rows.Err()
}

// MarkDatasourceSyncRequested flips the source to 'pending' and stamps the
// request time. Returns (nil, nil) when no own-org row matches (RLS-confined).
// Runs under the caller's WithOrgTx.
func (s *PostgresStore) MarkDatasourceSyncRequested(ctx context.Context, id string) (*business.Datasource, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx, `
		UPDATE datasources
		   SET sync_status = 'pending', last_sync_requested_at = NOW(), updated_at = NOW()
		 WHERE id = $1
		RETURNING `+datasourceColumns, id)
	ds, err := scanDatasource(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ds, nil
}
