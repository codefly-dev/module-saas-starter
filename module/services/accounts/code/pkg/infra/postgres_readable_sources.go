package infra

import (
	"context"
	"fmt"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
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
//
// resources names the permission resource types the calling module's content is
// governed by; it comes from that module's declared grant, because the host
// holds no domain content and so cannot name the resource itself.
//
// An empty set authorizes nothing. That needs saying in the query rather than
// left to the resource comparison: a wildcard ('*') role permission matches
// every resource type on its own, so without the cardinality guard a module
// that declared no content would still read every collection a wildcard role
// covers. The guard is what makes "declared none reads nothing" true.
func (s *PostgresStore) ListReadableSourcesPage(ctx context.Context, org string, subjects []string, resources []string, after string, limit int) ([]*gen.ReadableSourceCollection, error) {
	tx, err := sourceReadExecutor(ctx)
	if err != nil {
		return nil, err
	}
	// A nil slice would bind as NULL, and both `= ANY(NULL)` and cardinality(NULL)
	// are NULL rather than false. Both refuse, but only an empty array says so in
	// the plan.
	if resources == nil {
		resources = []string{}
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text, s.boundary_node_id::text, s.provider,
 COALESCE(s.repo,''), COALESCE(s.branch,''), s.paths, COALESCE(n.label,''),
 s.last_ingested_at, COALESCE(s.last_ingested_commit,''), COALESCE(s.last_delivery_id,'')
 FROM datasource_sources s JOIN scope_nodes n ON n.id=s.boundary_node_id AND n.org_id=s.org_id
 WHERE s.org_id=$1 AND ($3::uuid IS NULL OR s.id > $3::uuid)
 AND NOT EXISTS (
 SELECT 1 FROM unnest($2::uuid[]) subject(id) WHERE NOT (
 EXISTS (SELECT 1 FROM scope_grants g JOIN role_permissions rp ON rp.role_id=g.role_id
 WHERE g.org_id=$1 AND g.scope_path @> n.scope_path
 AND (g.expires_at IS NULL OR g.expires_at > now())
 AND cardinality($5::text[])>0 AND (rp.resource='*' OR rp.resource=ANY($5::text[])) AND (rp.action='*' OR rp.action='read')
 AND ((g.subject_kind='principal' AND g.subject_id=subject.id) OR
 (g.subject_kind='team' AND g.subject_id IN (SELECT team_id FROM team_members WHERE user_id=subject.id))))
 OR EXISTS (SELECT 1 FROM record_shares sh JOIN role_permissions rp ON rp.role_id=sh.role_id
 WHERE sh.org_id=$1 AND sh.resource_type=n.resource_type AND sh.resource_id=n.resource_id AND sh.resource_type=ANY($5::text[])
 AND (sh.expires_at IS NULL OR sh.expires_at > now())
 AND (rp.resource='*' OR rp.resource=ANY($5::text[])) AND (rp.action='*' OR rp.action='read')
 AND ((sh.subject_kind='principal' AND sh.subject_id=subject.id) OR
 (sh.subject_kind='team' AND sh.subject_id IN (SELECT team_id FROM team_members WHERE user_id=subject.id))))
 )) ORDER BY s.id LIMIT $4`, org, subjects, nullableSourceCursor(after), limit, resources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*gen.ReadableSourceCollection
	for rows.Next() {
		source := &gen.ReadableSourceCollection{}
		var enqueuedAt *time.Time
		var revision, delivery string
		if err := rows.Scan(&source.SourceId, &source.BoundaryId, &source.Origin, &source.Container, &source.Ref, &source.Paths,
			&source.BoundaryLabel, &enqueuedAt, &revision, &delivery); err != nil {
			return nil, err
		}
		// Container is the identity the consuming module's ingest keys this
		// source's entries under, so it has to be the one the ingest seam
		// delivers for the provider. GitHub deliveries name the repository
		// (github.repo), so the row's repo is the container and a GitHub row
		// without one cannot be attributed. Every other provider's deliveries
		// name the resource (api.url, crawler.url, upload.bucket+upload.key)
		// and the source (datasource.source_id) and no container of their own;
		// the source id is the one container-level identity they carry, so it
		// is the container. A bucket is not one: two sources over the same
		// bucket are two containers. repo is written by the GitHub branch of
		// AddSource alone, so for these rows it is never consulted.
		if source.Origin == "github" {
			if source.Container == "" {
				return nil, status.Error(codes.FailedPrecondition, "source attribution is incomplete")
			}
		} else {
			source.Container = source.SourceId
		}
		if source.Ref != "" && !strings.HasPrefix(source.Ref, "refs/") {
			source.Ref = "refs/heads/" + source.Ref
		}
		if enqueuedAt != nil {
			source.Sync = &gen.CollectionSyncProvenance{
				Stage:    gen.SourceSyncStage_SOURCE_SYNC_STAGE_CHANGES_ENQUEUED,
				At:       timestamppb.New(*enqueuedAt),
				Revision: revision,
				Trigger:  delivery,
			}
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

// ReadableCollectionGrants returns, per boundary node, the active grants that
// confer read on that collection's content — the same scope-tree grants
// ListCollectionAccess projects to an organization administrator, intersected
// with the calling module's declared resource types so the two surfaces cannot
// disagree about what a read grant is.
//
// A grant is reported for the boundary whenever its path is an ancestor-or-self
// of the boundary's, which is how scope_grants inherit; `inherited` is the
// strict-ancestor case, so a consumer can tell a grant made on this collection
// from one it received from above.
func (s *PostgresStore) ReadableCollectionGrants(ctx context.Context, org string, boundaries []string, resources []string) (map[string][]*gen.ReadableCollectionGrant, error) {
	tx, err := sourceReadExecutor(ctx)
	if err != nil {
		return nil, err
	}
	if resources == nil {
		resources = []string{}
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,
 COALESCE(t.name, p.display_name, g.subject_id::text), g.subject_kind, r.name,
 g.scope_path::text, g.scope_path <> n.scope_path, g.expires_at
 FROM scope_nodes n JOIN scope_grants g ON g.org_id=n.org_id AND g.scope_path @> n.scope_path
 JOIN roles r ON r.id=g.role_id
 LEFT JOIN principals p ON g.subject_kind='principal' AND p.id=g.subject_id
 LEFT JOIN teams t ON g.subject_kind='team' AND t.id=g.subject_id
 WHERE n.org_id=$1 AND n.id=ANY($2::uuid[])
 AND (g.expires_at IS NULL OR g.expires_at > now())
 AND cardinality($3::text[])>0
 AND EXISTS (SELECT 1 FROM role_permissions rp WHERE rp.role_id=g.role_id
 AND (rp.resource='*' OR rp.resource=ANY($3::text[])) AND (rp.action='*' OR rp.action='read'))
 ORDER BY n.id, g.created_at, g.id`, org, boundaries, resources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]*gen.ReadableCollectionGrant, len(boundaries))
	for rows.Next() {
		var boundary string
		grant := &gen.ReadableCollectionGrant{}
		var expires *time.Time
		if err := rows.Scan(&boundary, &grant.SubjectLabel, &grant.SubjectKind, &grant.RoleName,
			&grant.ScopePath, &grant.Inherited, &expires); err != nil {
			return nil, err
		}
		if expires != nil {
			grant.ExpiresAt = timestamppb.New(*expires)
		}
		out[boundary] = append(out[boundary], grant)
	}
	return out, rows.Err()
}

// LatestSourceSyncRequests returns, per source, when a sync was last requested
// and by whom. ADR 0008 keeps that actor in the audit trail and nowhere else, so
// this reads the trail rather than a column, and it is a separate occurrence
// from the ingest stage the source row records.
//
// The lookup is per source rather than one grouped scan. audit_events is range
// partitioned on created_at with no bound this read could supply — the last
// request may be arbitrarily old — so a DISTINCT ON over the whole set has to
// read every matching row in every retained partition and sort it to keep one
// row per source: measured at 40k rows sorted to return 100. Per source, the
// (org_id, resource, resource_id, created_at DESC) index yields the newest row
// first and LIMIT 1 stops there.
//
// Ordering on created_at alone is what keeps that index usable. The id tiebreak
// it replaces was not a "later" ordering to begin with — ids are random v4 —
// only an arbitrary stable one, and two requests for one source landing on the
// same timestamp are separate transactions with equally true answers.
func (s *PostgresStore) LatestSourceSyncRequests(ctx context.Context, org string, sources []string) (map[string]business.SourceSyncRequest, error) {
	tx, err := sourceReadExecutor(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT requested.id, event.created_at,
 COALESCE(p.display_name, event.actor_id::text, '')
 FROM unnest($2::text[]) AS requested(id)
 CROSS JOIN LATERAL (
 SELECT a.created_at, a.actor_id FROM audit_events a
 WHERE a.org_id=$1 AND a.resource='datasource' AND a.resource_id=requested.id
 AND a.event_type=$3
 ORDER BY a.created_at DESC LIMIT 1) event
 LEFT JOIN principals p ON p.id=event.actor_id`, org, sources, string(business.EventDatasourceSourceSynced))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]business.SourceSyncRequest, len(sources))
	for rows.Next() {
		var source string
		var request business.SourceSyncRequest
		if err := rows.Scan(&source, &request.RequestedAt, &request.RequestedBy); err != nil {
			return nil, err
		}
		out[source] = request
	}
	return out, rows.Err()
}
