package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/jackc/pgx/v5"
)

func installerConflict(reason string) error {
	return business.NewStoreError(errors.New(reason), business.ErrTypeConflict)
}

// ReconcileModuleInstallation runs inside the business service's tenant
// transaction. The lock also covers the existing human InstallSolution path.
// It serializes the complete compare/create transaction across Accounts replicas.
func (s *PostgresStore) ReconcileModuleInstallation(ctx context.Context, p *business.InstallSolutionParams, permissions []string, apply bool) (*business.ModuleInstallationResult, error) {
	executor := s.getQueryExecutor(ctx)
	if err := lockSolutionInstallation(ctx, executor, p.OrgID, p.SolutionIdentifier); err != nil {
		return nil, err
	}
	var admin bool
	err := executor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.organization_eligible_administrators($1) AS administrator WHERE administrator=$2)`, p.OrgID, p.OwnerPrincipalID).Scan(&admin)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, installerConflict("delegation owner is no longer an organization administrator")
	}
	var rolePermissions []string
	if err = executor.QueryRow(ctx, `SELECT COALESCE(array_agg(rp.resource || ':' || rp.action ORDER BY rp.resource, rp.action) FILTER (WHERE rp.resource IS NOT NULL), ARRAY[]::text[])
 FROM roles r LEFT JOIN role_permissions rp ON rp.role_id=r.id
 WHERE r.id=$1 AND (r.org_id=$2 OR r.org_id IS NULL) GROUP BY r.id`, p.RoleID, p.OrgID).Scan(&rolePermissions); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, installerConflict("approved role is missing")
		}
		return nil, err
	}
	if !sameStringSet(permissions, rolePermissions) {
		return nil, installerConflict("approved role permissions have changed")
	}
	existing, err := scanInstallation(executor.QueryRow(ctx, `SELECT `+installationColumns+` FROM installations WHERE org_id=$1 AND solution_identifier=$2 ORDER BY created_at DESC LIMIT 1`, p.OrgID, p.SolutionIdentifier))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if existing != nil {
		var owner string
		if err = executor.QueryRow(ctx, `SELECT COALESCE(installer_principal_id::text,'') FROM installations WHERE id=$1 AND org_id=$2`, existing.Id, p.OrgID).Scan(&owner); err != nil {
			return nil, err
		}
		if owner != p.InstallerPrincipalID || existing.OwnerPrincipalId != p.OwnerPrincipalID {
			return nil, installerConflict("installation belongs to another owner; automatic adoption is forbidden")
		}
		if existing.Status != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
			return nil, installerConflict("installation was revoked; explicit lifecycle action is required")
		}
		if err = reconcileExistingInstallation(ctx, executor, existing, p); err != nil {
			return nil, err
		}
		health, err := s.resolveInstallationHealth(ctx, executor, existing)
		if err != nil {
			return nil, err
		}
		if health != gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY {
			return nil, installerConflict("installation is unhealthy: " + health.String())
		}
		return installerResult(ctx, executor, existing, false)
	}
	// Never silently adopt an agent made by another operation, even if its
	// identifier and permissions happen to match. Revoked historical agents also
	// occupy their immutable identifier and require an explicit lifecycle review.
	var occupied bool
	if err = executor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE org_id=$1 AND agent_identifier=$2 AND kind='agent')`, p.OrgID, p.AgentIdentifier).Scan(&occupied); err != nil {
		return nil, err
	}
	if occupied {
		return nil, installerConflict("agent identifier already exists outside this installation; automatic adoption is forbidden")
	}
	if !apply {
		return &business.ModuleInstallationResult{State: "absent", OrganizationID: p.OrgID}, nil
	}
	installation, err := s.InstallSolution(ctx, p)
	if err != nil {
		return nil, err
	}
	return installerResult(ctx, executor, installation, true)
}

func installerResult(ctx context.Context, executor QueryExecutor, i *gen.Installation, changed bool) (*business.ModuleInstallationResult, error) {
	var ids []string
	if err := executor.QueryRow(ctx, `SELECT COALESCE(array_agg(CASE WHEN g.scope_path=n.scope_path AND g.expires_at IS NULL THEN g.id::text ELSE '' END),ARRAY[]::text[]) FROM scope_grants g JOIN scope_nodes n ON n.id=$3 WHERE g.org_id=$1 AND g.subject_kind='principal' AND g.subject_id=$2`, i.OrgId, i.AgentPrincipalId, i.RootScopeNodeId).Scan(&ids); err != nil {
		return nil, err
	}
	if len(ids) != 1 || ids[0] == "" {
		return nil, installerConflict("installation must have exactly one nonexpiring standing grant at its own root; unrelated grants require review")
	}
	return &business.ModuleInstallationResult{State: "ready", Changed: changed, OrganizationID: i.OrgId, PrincipalID: i.AgentPrincipalId, InstallationID: i.Id, ScopeNodeID: i.RootScopeNodeId, GrantID: ids[0]}, nil
}

func lockSolutionInstallation(ctx context.Context, executor QueryExecutor, orgID, solution string) error {
	_, err := executor.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "installation/"+orgID+"/"+solution)
	if err != nil {
		return fmt.Errorf("serialize installation: %w", err)
	}
	return nil
}
