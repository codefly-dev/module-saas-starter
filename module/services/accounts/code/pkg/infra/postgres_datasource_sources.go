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
	boundary_node_id::text, credential_secret_ref, COALESCE(webhook_secret_ref, ''),
	status, COALESCE(status_reason, ''), last_synced_at, created_at, updated_at, config,
	COALESCE(last_ingested_commit, ''), last_ingested_at, COALESCE(last_delivery_id, ''),
	(EXTRACT(EPOCH FROM reconcile_interval))::bigint, next_reconcile_at`

func scanDatasourceSource(row pgx.Row) (*business.DatasourceSource, error) {
	var d business.DatasourceSource
	var config []byte
	var reconcileIntervalSeconds int64
	if err := row.Scan(
		&d.ID, &d.OrgID, &d.Provider, &d.Repo, &d.Paths, &d.Branch,
		&d.BoundaryNodeID, &d.CredentialSecretRef, &d.WebhookSecretRef,
		&d.Status, &d.StatusReason, &d.LastSyncedAt, &d.CreatedAt, &d.UpdatedAt, &config,
		&d.LastIngestedCommit, &d.LastIngestedAt, &d.LastDeliveryID,
		&reconcileIntervalSeconds, &d.NextReconcileAt,
	); err != nil {
		return nil, err
	}
	d.ReconcileInterval = time.Duration(reconcileIntervalSeconds) * time.Second
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
			id, org_id, provider, repo, paths, branch, boundary_node_id,
			credential_secret_ref, webhook_secret_ref, status, config,
			reconcile_interval, next_reconcile_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, NULLIF($6, ''), $7, $8, NULLIF($9, ''), $10, $11,
			make_interval(secs => $12), $13)`,
		source.ID, source.OrgID, source.Provider, source.Repo, paths, source.Branch,
		source.BoundaryNodeID, source.CredentialSecretRef, source.WebhookSecretRef, source.Status, config,
		source.ReconcileInterval.Seconds(), source.NextReconcileAt,
	)
	return err
}

// AdvanceDatasourceCursor records the head commit fully enqueued as a change set
// and reschedules the periodic reconcile. next_reconcile_at is pushed out by the
// reconcile interval, or cleared when reconcile is disabled (interval 0). Runs
// under the caller's WithControlPlane.
func (s *PostgresStore) AdvanceDatasourceCursor(ctx context.Context, sourceID, commit, deliveryID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_sources
		   SET last_ingested_commit = $2,
		       last_ingested_at     = NOW(),
		       last_delivery_id      = NULLIF($3, ''),
		       next_reconcile_at     = CASE WHEN reconcile_interval > INTERVAL '0'
		                                    THEN NOW() + reconcile_interval END,
		       updated_at            = NOW()
		 WHERE id = $1`, sourceID, commit, deliveryID)
	return err
}

// BumpDatasourceReconcile reschedules the periodic reconcile without touching the
// cursor. Runs under the caller's WithControlPlane.
func (s *PostgresStore) BumpDatasourceReconcile(ctx context.Context, sourceID string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_sources
		   SET next_reconcile_at = CASE WHEN reconcile_interval > INTERVAL '0'
		                                THEN NOW() + reconcile_interval END,
		       updated_at        = NOW()
		 WHERE id = $1`, sourceID)
	return err
}

// MarkDatasourceSourceDegraded parks a source the compiler cannot make progress
// on for a structural reason an operator must resolve (an oversized snapshot
// manifest). Flipping status off 'active' removes it from the reconcile sweep and
// its partial index, so it stops being re-selected — no schedule bump. Clearing
// next_reconcile_at keeps the row out of the due set even after an operator
// widens the interval. Runs under the caller's WithControlPlane (the leased
// compiler has no tenant context).
func (s *PostgresStore) MarkDatasourceSourceDegraded(ctx context.Context, sourceID, reason string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_sources
		   SET status            = 'degraded',
		       status_reason     = $2,
		       next_reconcile_at = NULL,
		       updated_at        = NOW()
		 WHERE id = $1`, sourceID, reason)
	return err
}

// ListDatasourceSourcesDueForReconcile returns active GitHub sources whose
// reconcile is due, oldest schedule first. Runs under the caller's WithControlPlane.
func (s *PostgresStore) ListDatasourceSourcesDueForReconcile(ctx context.Context, now time.Time, limit int) ([]*business.DatasourceSource, error) {
	rows, err := s.getQueryExecutor(ctx).Query(ctx,
		`SELECT `+datasourceSourceColumns+`
		   FROM datasource_sources
		  WHERE status = 'active'
		    AND provider = 'github'
		    AND next_reconcile_at IS NOT NULL
		    AND next_reconcile_at <= $1
		  ORDER BY next_reconcile_at
		  LIMIT $2`, now, limit)
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

// LockDatasourceSourceCredentialRef reads the credential envelope under a row
// lock so a refresh-and-rotate cycle (OAuth 2.0) serializes against a concurrent
// sync of the same source: the second caller blocks here until the first commits,
// then reads the freshly rotated envelope and skips its own refresh. Runs under
// the caller's WithOrgTx; the FOR UPDATE lock is held until that transaction
// commits. Returns ErrNoRows when the source is gone.
func (s *PostgresStore) LockDatasourceSourceCredentialRef(ctx context.Context, orgID, id string) (string, error) {
	var ref string
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `
		SELECT credential_secret_ref
		  FROM datasource_sources
		 WHERE org_id = $1 AND id = $2
		   FOR UPDATE`, orgID, id).Scan(&ref)
	if err != nil {
		return "", err
	}
	return ref, nil
}

// UpdateDatasourceSourceCredential rotates the stored credential envelope in
// place, for a connector (OAuth 2.0) that refreshes and re-persists its token
// set at fetch time. Runs under the caller's WithOrgTx.
func (s *PostgresStore) UpdateDatasourceSourceCredential(ctx context.Context, orgID, id, credentialRef string) error {
	_, err := s.getQueryExecutor(ctx).Exec(ctx, `
		UPDATE datasource_sources
		   SET credential_secret_ref = $3, updated_at = NOW()
		 WHERE org_id = $1 AND id = $2`, orgID, id, credentialRef)
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
