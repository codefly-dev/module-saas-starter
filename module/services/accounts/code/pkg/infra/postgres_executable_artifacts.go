package infra

import (
	"accounts/pkg/auth"
	"accounts/pkg/business"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

const artifactApprovalColumns = `id::text, org_id::text, installation_id::text, module_principal_id::text, policy_id, subject_digest, subject, approved_by::text, created_at, revoked_at`

func scanArtifactApproval(row pgx.Row) (*business.ExecutableArtifactApproval, error) {
	var out business.ExecutableArtifactApproval
	err := row.Scan(&out.ID, &out.OrgID, &out.Installation, &out.Module, &out.Policy, &out.Digest, &out.Subject, &out.ApprovedBy, &out.CreatedAt, &out.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func (s *PostgresStore) PutExecutableArtifactApproval(ctx context.Context, a *business.ExecutableArtifactApproval) (*business.ExecutableArtifactApproval, bool, error) {
	result, err := s.getQueryExecutor(ctx).Exec(ctx, `INSERT INTO executable_artifact_approvals
 (id,org_id,installation_id,module_principal_id,policy_id,subject_digest,subject,approved_by)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
 ON CONFLICT (org_id,installation_id,module_principal_id,subject_digest) DO NOTHING`, a.ID, a.OrgID, a.Installation, a.Module, a.Policy, a.Digest, a.Subject, a.ApprovedBy)
	if err != nil {
		return nil, false, err
	}
	row, err := s.GetExecutableArtifactApproval(ctx, a.OrgID, a.Installation, a.Module, a.Digest)
	return row, result.RowsAffected() == 1, err
}
func (s *PostgresStore) GetExecutableArtifactApproval(ctx context.Context, org, installation, module, digest string) (*business.ExecutableArtifactApproval, error) {
	// Serialize exact-identity decisions with revocation. FOR UPDATE avoids lock
	// upgrades when this lookup precedes a revoke in the same tenant transaction.
	return scanArtifactApproval(s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT `+artifactApprovalColumns+`
 FROM executable_artifact_approvals WHERE org_id=$1 AND installation_id=$2 AND module_principal_id=$3 AND subject_digest=$4 FOR UPDATE`, org, installation, module, digest))
}
func (s *PostgresStore) RevokeExecutableArtifactApproval(ctx context.Context, org, installation, module, digest, actor string) (bool, error) {
	result, err := s.getQueryExecutor(ctx).Exec(ctx, `UPDATE executable_artifact_approvals SET revoked_at=CURRENT_TIMESTAMP, revoked_by=$5
 WHERE org_id=$1 AND installation_id=$2 AND module_principal_id=$3 AND subject_digest=$4 AND revoked_at IS NULL`, org, installation, module, digest, actor)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() == 1, nil
}

func (s *PostgresStore) GetExecutableArtifactApprovalByID(ctx context.Context, org, installation, module, id string) (*business.ExecutableArtifactApproval, error) {
	return scanArtifactApproval(s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT `+artifactApprovalColumns+` FROM executable_artifact_approvals WHERE org_id=$1 AND installation_id=$2 AND module_principal_id=$3 AND id=$4 FOR UPDATE`, org, installation, module, id))
}

// Uses the same permission decision as Work Context issuance, including admin,
// member-contributed, team and exact-resource grants. No impersonation or
// cross-user readAs call is needed: actor is the current authenticated parent.
func (s *PostgresStore) CheckExecutableArtifactAuthority(ctx context.Context, org, actor, installation string, p business.ArtifactPermission, admin bool) (bool, error) {
	if err := auth.RequireVerifiedDatabaseScope(ctx, org, actor); err != nil {
		return false, err
	}
	executor := s.getQueryExecutor(ctx)
	var active bool
	err := executor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_members m JOIN users u ON u.uuid=m.user_id WHERE m.org_id=$1 AND m.user_id=$2 AND u.status='active' AND (NOT $3 OR m.role IN ('owner','admin')))`, org, actor, admin).Scan(&active)
	if err != nil || !active {
		return false, err
	}
	return workContextPermissionAllowed(ctx, executor, org, actor, true, business.WorkContextPermission{ResourceKind: p.Resource, Action: p.Action, ResourceID: installation}, false)
}
