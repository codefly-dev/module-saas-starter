package infra

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// scopePathPattern mirrors the scope_nodes_label_charset CHECK: ltree labels are
// the lowercase set [a-z0-9_] joined by dots. Raw UUIDs (hyphens) are not valid
// labels and must be encoded (lowercase, '-' -> '_') before use as a path.
// Validating here turns an opaque 23514 CHECK violation into a clean
// InvalidArgument at the boundary.
var scopePathPattern = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)

func validateScopePath(path string) error {
	if !scopePathPattern.MatchString(path) {
		return status.Errorf(codes.InvalidArgument,
			"scope_path %q is not a valid ltree path of [a-z0-9_] labels", path)
	}
	return nil
}

// subjectKindColumn maps the proto enum to the stored subject_kind text, shared
// by every layered-access write path.
func subjectKindColumn(kind gen.SubjectKind) (string, error) {
	switch kind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		return "principal", nil
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		return "team", nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unsupported subject kind %s", kind)
	}
}

// layeredSubjectPredicate builds the subject-match SQL for a given table alias,
// identical in shape to CheckPermission: a human principal also inherits grants
// assigned to teams they belong to. The alias is interpolated via %[1]s so the
// same predicate can guard both the scope-grant and record-share branches.
func layeredSubjectPredicate(kind gen.SubjectKind, alias string) (string, error) {
	switch kind {
	case gen.SubjectKind_SUBJECT_KIND_PRINCIPAL:
		return fmt.Sprintf(`(
			(%[1]s.subject_kind = 'principal' AND %[1]s.subject_id = $1)
			OR (%[1]s.subject_kind = 'team' AND %[1]s.subject_id IN (
				SELECT team_id FROM team_members WHERE user_id = $1))
		)`, alias), nil
	case gen.SubjectKind_SUBJECT_KIND_TEAM:
		return fmt.Sprintf(`(%[1]s.subject_kind = 'team' AND %[1]s.subject_id = $1)`, alias), nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unsupported subject kind %s", kind)
	}
}

// CheckAccess reports whether subject may perform (resourceType, action) on a
// specific record, via either a hierarchical scope grant at any ancestor of the
// record's scope path, or a direct per-record share. It is the hierarchical +
// per-record companion to CheckPermission; org-wide/flat capability still comes
// from CheckPermission, which is untouched.
//
// SECURITY: the record's scope path is resolved HERE, from the record's own
// registered scope_nodes row (keyed by resource_type + resource_id under the RLS
// tenant floor) — never from a caller-supplied path. A caller entitled at one
// scope therefore cannot authorize a resource that actually lives under a
// different scope (RFC-0001 open-question 2).
//
// Runs inside WithOrgTx: app.current_org_id is set, so RLS confines every table
// below to the caller's tenant.
func (s *PostgresStore) CheckAccess(ctx context.Context, subjectID string, subjectKind gen.SubjectKind, resourceType, resourceID, action string) (bool, string, error) {
	w := wool.Get(ctx).In("CheckAccess")
	executor := s.getQueryExecutor(ctx)

	scopePred, err := layeredSubjectPredicate(subjectKind, "g")
	if err != nil {
		return false, "", err
	}
	sharePred, err := layeredSubjectPredicate(subjectKind, "sh")
	if err != nil {
		return false, "", err
	}

	// $1 subject, $2 resource_type, $3 resource_id, $4 action.
	// Scope branch: a grant whose scope_path is an ancestor-or-equal of the
	// record's resolved path (g.scope_path @> record.scope_path). If the record
	// has no registered node the CTE is empty and the CROSS JOIN yields no rows,
	// so the scope branch fails closed while the share branch can still grant.
	// A NULL-safe wildcard role_permissions ('*') matches, as in CheckPermission.
	query := `
		WITH record AS (
			SELECT scope_path FROM scope_nodes
			WHERE resource_type = $2 AND resource_id = $3
			LIMIT 1
		)
		SELECT 'scope' AS via
		FROM scope_grants g
		JOIN role_permissions rp ON rp.role_id = g.role_id
		CROSS JOIN record
		WHERE ` + scopePred + `
		  AND g.scope_path @> record.scope_path
		  AND (g.expires_at IS NULL OR g.expires_at > now())
		  AND (rp.resource = '*' OR rp.resource = $2)
		  AND (rp.action   = '*' OR rp.action   = $4)
		UNION ALL
		SELECT 'share' AS via
		FROM record_shares sh
		JOIN role_permissions rp ON rp.role_id = sh.role_id
		WHERE ` + sharePred + `
		  AND sh.resource_type = $2
		  AND sh.resource_id   = $3
		  AND (sh.expires_at IS NULL OR sh.expires_at > now())
		  AND (rp.resource = '*' OR rp.resource = $2)
		  AND (rp.action   = '*' OR rp.action   = $4)
		LIMIT 1`

	var via string
	err = executor.QueryRow(ctx, query, subjectID, resourceType, resourceID, action).Scan(&via)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, "no scope grant or record share", nil
		}
		return false, "", w.Wrapf(err, "failed to check access")
	}
	return true, "granted via " + via, nil
}

// RegisterScopeNode inserts a node into the org's scope tree, or places a
// product record at a node when ResourceType/ResourceId are set. node.Id and
// node.OrgId are set by the caller.
func (s *PostgresStore) RegisterScopeNode(ctx context.Context, node *gen.ScopeNode) error {
	w := wool.Get(ctx).In("RegisterScopeNode")
	executor := s.getQueryExecutor(ctx)

	if err := validateScopePath(node.ScopePath); err != nil {
		return err
	}

	var resourceType, resourceID any
	if node.ResourceType != "" || node.ResourceId != "" {
		if node.ResourceType == "" || node.ResourceId == "" {
			return status.Error(codes.InvalidArgument, "resource_type and resource_id must be set together")
		}
		resourceType = node.ResourceType
		resourceID = node.ResourceId
	}

	var createdAt time.Time
	err := executor.QueryRow(ctx, `
		INSERT INTO scope_nodes (id, org_id, scope_path, kind, label, resource_type, resource_id, created_at)
		VALUES ($1, $2, $3::ltree, $4, $5, $6, $7, NOW())
		RETURNING created_at`,
		node.Id, node.OrgId, node.ScopePath, node.Kind, node.Label, resourceType, resourceID,
	).Scan(&createdAt)
	if err != nil {
		return w.Wrapf(err, "failed to register scope node")
	}
	node.CreatedAt = timestamppb.New(createdAt)
	return nil
}

// GrantScope inserts a hierarchical scope grant. The scope path must already be
// a registered node in this org (gap 6: granting on an unregistered scope
// fails). grant.Id and grant.OrgId are set by the caller.
func (s *PostgresStore) GrantScope(ctx context.Context, grant *gen.ScopeGrant) error {
	w := wool.Get(ctx).In("GrantScope")
	executor := s.getQueryExecutor(ctx)

	if err := validateScopePath(grant.ScopePath); err != nil {
		return err
	}
	kind, err := subjectKindColumn(grant.SubjectKind)
	if err != nil {
		return err
	}

	var exists bool
	if err := executor.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM scope_nodes WHERE scope_path = $1::ltree)`,
		grant.ScopePath,
	).Scan(&exists); err != nil {
		return w.Wrapf(err, "failed to check scope node")
	}
	if !exists {
		return status.Errorf(codes.FailedPrecondition, "scope %q is not a registered node", grant.ScopePath)
	}

	var grantedBy, expiresAt any
	if grant.GrantedBy != "" {
		grantedBy = grant.GrantedBy
	}
	if grant.ExpiresAt != nil {
		expiresAt = grant.ExpiresAt.AsTime()
	}

	var createdAt time.Time
	err = executor.QueryRow(ctx, `
		INSERT INTO scope_grants (id, org_id, subject_id, subject_kind, scope_path, role_id, granted_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5::ltree, $6, $7, $8, NOW())
		RETURNING created_at`,
		grant.Id, grant.OrgId, grant.SubjectId, kind, grant.ScopePath, grant.RoleId, grantedBy, expiresAt,
	).Scan(&createdAt)
	if err != nil {
		return w.Wrapf(err, "failed to grant scope")
	}
	grant.CreatedAt = timestamppb.New(createdAt)
	return nil
}

// RevokeScope removes a hierarchical scope grant matching the exact tuple.
func (s *PostgresStore) RevokeScope(ctx context.Context, orgID, subjectID string, subjectKind gen.SubjectKind, scopePath, roleID string) error {
	w := wool.Get(ctx).In("RevokeScope")
	executor := s.getQueryExecutor(ctx)

	kind, err := subjectKindColumn(subjectKind)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `
		DELETE FROM scope_grants
		WHERE org_id = $1 AND subject_id = $2 AND subject_kind = $3
		  AND scope_path = $4::ltree AND role_id = $5`,
		orgID, subjectID, kind, scopePath, roleID,
	)
	if err != nil {
		return w.Wrapf(err, "failed to revoke scope grant")
	}
	return nil
}

// ShareRecord inserts a per-record share. share.Id and share.OrgId are set by
// the caller.
func (s *PostgresStore) ShareRecord(ctx context.Context, share *gen.RecordShare) error {
	w := wool.Get(ctx).In("ShareRecord")
	executor := s.getQueryExecutor(ctx)

	kind, err := subjectKindColumn(share.SubjectKind)
	if err != nil {
		return err
	}
	var grantedBy, expiresAt any
	if share.GrantedBy != "" {
		grantedBy = share.GrantedBy
	}
	if share.ExpiresAt != nil {
		expiresAt = share.ExpiresAt.AsTime()
	}

	var createdAt time.Time
	err = executor.QueryRow(ctx, `
		INSERT INTO record_shares (id, org_id, resource_type, resource_id, subject_id, subject_kind, role_id, granted_by, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		RETURNING created_at`,
		share.Id, share.OrgId, share.ResourceType, share.ResourceId,
		share.SubjectId, kind, share.RoleId, grantedBy, expiresAt,
	).Scan(&createdAt)
	if err != nil {
		return w.Wrapf(err, "failed to share record")
	}
	share.CreatedAt = timestamppb.New(createdAt)
	return nil
}

// RevokeShare removes a per-record share matching the exact tuple.
func (s *PostgresStore) RevokeShare(ctx context.Context, orgID, resourceType, resourceID, subjectID string, subjectKind gen.SubjectKind, roleID string) error {
	w := wool.Get(ctx).In("RevokeShare")
	executor := s.getQueryExecutor(ctx)

	kind, err := subjectKindColumn(subjectKind)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `
		DELETE FROM record_shares
		WHERE org_id = $1 AND resource_type = $2 AND resource_id = $3
		  AND subject_id = $4 AND subject_kind = $5 AND role_id = $6`,
		orgID, resourceType, resourceID, subjectID, kind, roleID,
	)
	if err != nil {
		return w.Wrapf(err, "failed to revoke record share")
	}
	return nil
}

// ListShares returns the shares on a specific record, newest first.
func (s *PostgresStore) ListShares(ctx context.Context, orgID, resourceType, resourceID string) ([]*gen.RecordShare, error) {
	w := wool.Get(ctx).In("ListShares")
	executor := s.getQueryExecutor(ctx)

	rows, err := executor.Query(ctx, `
		SELECT id, org_id, resource_type, resource_id, subject_id, subject_kind, role_id, granted_by, expires_at, created_at
		FROM record_shares
		WHERE org_id = $1 AND resource_type = $2 AND resource_id = $3
		ORDER BY created_at DESC`,
		orgID, resourceType, resourceID,
	)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list record shares")
	}
	defer rows.Close()

	var out []*gen.RecordShare
	for rows.Next() {
		var (
			id, oid, rtype, rid, subjID, kind, roleID string
			grantedBy                                 *string
			expiresAt                                 *time.Time
			createdAt                                 time.Time
		)
		if err := rows.Scan(&id, &oid, &rtype, &rid, &subjID, &kind, &roleID, &grantedBy, &expiresAt, &createdAt); err != nil {
			return nil, w.Wrapf(err, "failed to scan record share")
		}
		share := &gen.RecordShare{
			Id:           id,
			OrgId:        oid,
			ResourceType: rtype,
			ResourceId:   rid,
			SubjectId:    subjID,
			RoleId:       roleID,
			CreatedAt:    timestamppb.New(createdAt),
		}
		switch kind {
		case "principal":
			share.SubjectKind = gen.SubjectKind_SUBJECT_KIND_PRINCIPAL
		case "team":
			share.SubjectKind = gen.SubjectKind_SUBJECT_KIND_TEAM
		default:
			return nil, fmt.Errorf("list record shares: unsupported stored subject kind %q", kind)
		}
		if grantedBy != nil {
			share.GrantedBy = *grantedBy
		}
		if expiresAt != nil {
			share.ExpiresAt = timestamppb.New(*expiresAt)
		}
		out = append(out, share)
	}
	if err := rows.Err(); err != nil {
		return nil, w.Wrapf(err, "iterating record share rows")
	}
	return out, nil
}
