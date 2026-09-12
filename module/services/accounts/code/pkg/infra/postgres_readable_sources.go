package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sourceReadSnapshotKey struct{}

// WithSourceReadSnapshot keeps authority, grant intersection and source paging
// on one database snapshot. Only this read-only path installs the private key.
func (s *PostgresStore) WithSourceReadSnapshot(ctx context.Context, org string, run func(context.Context) error) error {
	_, owner, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok {
		return auth.ErrVerifiedDatabaseIdentityRequired
	}
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, owner); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // committed transactions cannot roll back
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_org_id', $1, true)", org); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, sourceReadSnapshotKey{}, tx)
	if err := run(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func sourceReadExecutor(ctx context.Context) (pgx.Tx, error) {
	tx, ok := ctx.Value(sourceReadSnapshotKey{}).(pgx.Tx)
	if !ok {
		return nil, fmt.Errorf("source read snapshot required")
	}
	return tx, nil
}

func (s *PostgresStore) SourceReadRevision(ctx context.Context, org string, subjects []string) (string, time.Time, error) {
	tx, err := sourceReadExecutor(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	var revision string
	var expires *time.Time
	err = tx.QueryRow(ctx, `SELECT concat_ws(':',
 COALESCE((SELECT revision FROM source_read_revisions WHERE org_id=$1),0),
 COALESCE((SELECT revision FROM organization_authorization_revisions WHERE org_id=$1),0),
 COALESCE((SELECT max(revision) FROM principal_authorization_revisions WHERE org_id=$1 AND principal_id=ANY($2::uuid[])),0)),
 LEAST(
 (SELECT min(expires_at) FROM scope_grants WHERE org_id=$1 AND expires_at > now()),
 (SELECT min(expires_at) FROM record_shares WHERE org_id=$1 AND expires_at > now()))`, org, subjects).Scan(&revision, &expires)
	if expires == nil {
		return revision, time.Time{}, err
	}
	return revision, *expires, err
}

// ListReadableSourcesPage intersects all principals in SQL before applying the
// keyset limit. No unrelated source or credential payload is materialized.
func (s *PostgresStore) ListReadableSourcesPage(ctx context.Context, org string, subjects []string, after string, limit int) ([]*gen.ReadableSourceCollection, error) {
	tx, err := sourceReadExecutor(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text, s.boundary_node_id::text, s.provider,
 COALESCE(s.repo,''), COALESCE(s.branch,''), s.paths
 FROM datasource_sources s JOIN scope_nodes n ON n.id=s.boundary_node_id AND n.org_id=s.org_id
 WHERE s.org_id=$1 AND ($3::uuid IS NULL OR s.id > $3::uuid)
 AND NOT EXISTS (
 SELECT 1 FROM unnest($2::uuid[]) subject(id) WHERE NOT (
 EXISTS (SELECT 1 FROM scope_grants g JOIN role_permissions rp ON rp.role_id=g.role_id
 WHERE g.org_id=$1 AND g.scope_path @> n.scope_path
 AND (g.expires_at IS NULL OR g.expires_at > now())
 AND (rp.resource='*' OR rp.resource='documents') AND (rp.action='*' OR rp.action='read')
 AND ((g.subject_kind='principal' AND g.subject_id=subject.id) OR
 (g.subject_kind='team' AND g.subject_id IN (SELECT team_id FROM team_members WHERE user_id=subject.id))))
 OR EXISTS (SELECT 1 FROM record_shares sh JOIN role_permissions rp ON rp.role_id=sh.role_id
 WHERE sh.org_id=$1 AND sh.resource_type=n.resource_type AND sh.resource_id=n.resource_id AND sh.resource_type='documents'
 AND (sh.expires_at IS NULL OR sh.expires_at > now())
 AND (rp.resource='*' OR rp.resource='documents') AND (rp.action='*' OR rp.action='read')
 AND ((sh.subject_kind='principal' AND sh.subject_id=subject.id) OR
 (sh.subject_kind='team' AND sh.subject_id IN (SELECT team_id FROM team_members WHERE user_id=subject.id))))
 )) ORDER BY s.id LIMIT $4`, org, subjects, nullableSourceCursor(after), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*gen.ReadableSourceCollection
	for rows.Next() {
		source := &gen.ReadableSourceCollection{}
		if err := rows.Scan(&source.SourceId, &source.BoundaryId, &source.Origin, &source.Container, &source.Ref, &source.Paths); err != nil {
			return nil, err
		}
		if source.Origin != "github" {
			return nil, status.Error(codes.Unimplemented, "source read projection is not supported for this provider")
		}
		if source.Container == "" {
			return nil, status.Error(codes.FailedPrecondition, "source attribution is incomplete")
		}
		if source.Ref != "" && !strings.HasPrefix(source.Ref, "refs/") {
			source.Ref = "refs/heads/" + source.Ref
		}
		out = append(out, source)
	}
	return out, rows.Err()
}
func nullableSourceCursor(after string) any {
	if after == "" {
		return nil
	}
	return after
}
