package infra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// installationColumns is the canonical SELECT list, uuid columns cast to text so
// they scan into strings (matching the gen.Installation wire shape).
const installationColumns = `id::text, org_id::text, agent_principal_id::text,
	solution_identifier, owner_principal_id::text, co_owner_principal_ids::text[],
	root_scope_node_id::text, status, created_at, revoked_at`

func scanInstallation(row rowScanner) (*gen.Installation, error) {
	var (
		id, orgID, agentID, solution, ownerID, rootNodeID, status string
		coOwners                                                  []string
		createdAt                                                 time.Time
		revokedAt                                                 *time.Time
	)
	if err := row.Scan(&id, &orgID, &agentID, &solution, &ownerID, &coOwners,
		&rootNodeID, &status, &createdAt, &revokedAt); err != nil {
		return nil, err
	}
	out := &gen.Installation{
		Id:                  id,
		OrgId:               orgID,
		AgentPrincipalId:    agentID,
		SolutionIdentifier:  solution,
		OwnerPrincipalId:    ownerID,
		CoOwnerPrincipalIds: coOwners,
		RootScopeNodeId:     rootNodeID,
		Status:              installationStatusFromColumn(status),
		CreatedAt:           timestamppb.New(createdAt),
	}
	if revokedAt != nil {
		out.RevokedAt = timestamppb.New(*revokedAt)
	}
	return out, nil
}

func installationStatusFromColumn(status string) gen.InstallationStatus {
	switch status {
	case "active":
		return gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE
	case "revoked":
		return gen.InstallationStatus_INSTALLATION_STATUS_REVOKED
	default:
		return gen.InstallationStatus_INSTALLATION_STATUS_UNSPECIFIED
	}
}

// isCurrentOrgAdmin reports whether principalID is a current owner/admin member
// of org. The single source of truth for "accountable human is still an admin".
func isCurrentOrgAdmin(ctx context.Context, executor QueryExecutor, orgID, principalID string) (bool, error) {
	var admin bool
	err := executor.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM organization_members
			WHERE org_id = $1 AND user_id = $2 AND role IN ('owner', 'admin')
		)`, orgID, principalID).Scan(&admin)
	return admin, err
}

// firstEligibleOwner returns the first candidate (owner of record, then co-owners
// in order) that is a current org admin, or "" when none is — the fail-closed
// "connection owner left the org" signal. Priority order is preserved so the owner
// of record wins when still eligible; a co-owner is used only as succession.
func firstEligibleOwner(ctx context.Context, executor QueryExecutor, orgID string, candidates []string) (string, error) {
	for _, candidate := range candidates {
		admin, err := isCurrentOrgAdmin(ctx, executor, orgID, candidate)
		if err != nil {
			return "", err
		}
		if admin {
			return candidate, nil
		}
	}
	return "", nil
}

// InstallSolution composes the agent principal, solution scope node, standing
// grant, and installation row inside the caller's WithOrgTx. Idempotent per
// (org, solution): a re-install of an active solution returns the existing row.
func (s *PostgresStore) InstallSolution(ctx context.Context, params *business.InstallSolutionParams) (*gen.Installation, error) {
	w := wool.Get(ctx).In("InstallSolution",
		wool.Field("org_id", params.OrgID),
		wool.Field("solution", params.SolutionIdentifier))
	executor := s.getQueryExecutor(ctx)

	// The owner of record must be a current org admin. The installing caller is
	// already gated as an admin; an explicitly named owner is validated here.
	admin, err := isCurrentOrgAdmin(ctx, executor, params.OrgID, params.OwnerPrincipalID)
	if err != nil {
		return nil, w.Wrapf(err, "failed to check owner org-admin status")
	}
	if !admin {
		return nil, business.NewStoreError(
			fmt.Errorf("owner of record %s is not a current org admin", params.OwnerPrincipalID),
			business.ErrTypeConflict,
		)
	}

	// Idempotent re-install: an active installation for this solution already
	// composed the agent, node, and grant. A re-install that names the same
	// authority envelope (ceiling, standing-grant role, co-owner set) returns the
	// existing row unchanged; one that names a different envelope is rejected rather
	// than silently ignored — a changed ceiling is applied by uninstall + reinstall.
	existing, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations
		 WHERE org_id = $1 AND solution_identifier = $2 AND status = 'active'`,
		params.OrgID, params.SolutionIdentifier))
	if err == nil {
		if e := reconcileExistingInstallation(ctx, executor, existing, params); e != nil {
			return nil, e
		}
		w.Trace("solution already installed; returning existing installation",
			wool.Field("installation_id", existing.Id))
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, w.Wrapf(err, "failed to check for existing installation")
	}

	// 1. Agent principal, idempotent per version.
	agentID, err := s.getOrCreateAgentPrincipal(ctx, params)
	if err != nil {
		return nil, err
	}

	// 2. Solution scope node with a server-derived, UUID-labelled path; reused if a
	// prior revoked install of this solution left one behind.
	nodeID, nodePath, err := s.getOrRegisterSolutionNode(ctx, params)
	if err != nil {
		return nil, err
	}

	// 3. The agent's standing, non-expiring, least-privilege grant at the node.
	grant := &gen.ScopeGrant{
		Id:          business.NewIDString(),
		OrgId:       params.OrgID,
		SubjectId:   agentID,
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ScopePath:   nodePath,
		RoleId:      params.RoleID,
		GrantedBy:   params.GrantedBy,
	}
	if err := s.GrantScope(ctx, grant); err != nil {
		return nil, w.Wrapf(err, "failed to grant standing scope to agent")
	}

	// 4. The installation row with its owner of record.
	installation, err := scanInstallation(executor.QueryRow(ctx, `
		INSERT INTO installations
			(id, org_id, agent_principal_id, solution_identifier, owner_principal_id,
			 co_owner_principal_ids, root_scope_node_id, status)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, 'active')
		RETURNING `+installationColumns,
		params.OrgID, agentID, params.SolutionIdentifier, params.OwnerPrincipalID,
		stringArray(params.CoOwnerPrincipalIDs), nodeID,
	))
	if err != nil {
		// A concurrent install of the same solution won the race between the
		// idempotent-existing check above and this INSERT; the active-solution
		// unique index rejects the second row. Surface it as a retryable conflict
		// (the caller's retry hits the idempotent path) rather than an opaque 500.
		if isUniqueViolation(err) {
			return nil, business.NewStoreError(
				fmt.Errorf("solution %s is already being installed in org %s", params.SolutionIdentifier, params.OrgID),
				business.ErrTypeConflict,
			)
		}
		return nil, w.Wrapf(err, "failed to insert installation")
	}
	return installation, nil
}

func (s *PostgresStore) getOrCreateAgentPrincipal(ctx context.Context, params *business.InstallSolutionParams) (string, error) {
	w := wool.Get(ctx).In("getOrCreateAgentPrincipal")
	existing, err := s.GetAgentPrincipal(ctx, params.OrgID, params.AgentIdentifier)
	if err == nil {
		return existing.ID, nil
	}
	var se *business.StoreError
	if !errors.As(err, &se) || se.StoreErrorType != business.ErrTypeNotFound {
		return "", w.Wrapf(err, "failed to look up agent principal")
	}
	displayName := params.DisplayName
	if displayName == "" {
		displayName = params.AgentIdentifier
	}
	agent := &business.Principal{
		ID:               business.NewIDString(),
		Kind:             business.PrincipalKindAgent,
		DisplayName:      displayName,
		OrgID:            params.OrgID,
		AgentIdentifier:  params.AgentIdentifier,
		CreatedBy:        params.GrantedBy,
		CreatedAt:        time.Now().UTC(),
		AllowedAudiences: params.AllowedAudiences,
		AllowedScopes:    params.AllowedScopes,
	}
	if err := s.CreateAgentPrincipal(ctx, agent); err != nil {
		return "", w.Wrapf(err, "failed to create agent principal")
	}
	return agent.ID, nil
}

// getOrRegisterSolutionNode returns the (id, ltree path) of the installation's
// authority-root scope node. On a reinstall it reuses the solution node a prior
// revoked install of this solution left behind (uninstall removes the grant and
// revokes the agent but leaves the node), so the authority root — and any paths
// registered beneath it — stay stable across reinstall. Otherwise it mints a fresh
// node whose path is a depth-1 ltree label derived from the node id (ADR-0002:
// labels are node UUIDs, never caller-chosen strings), which makes collisions and
// nesting between installs impossible.
func (s *PostgresStore) getOrRegisterSolutionNode(ctx context.Context, params *business.InstallSolutionParams) (string, string, error) {
	w := wool.Get(ctx).In("getOrRegisterSolutionNode")
	executor := s.getQueryExecutor(ctx)
	var nodeID, nodePath string
	err := executor.QueryRow(ctx, `
		SELECT n.id::text, n.scope_path::text
		FROM installations i
		JOIN scope_nodes n ON n.id = i.root_scope_node_id
		WHERE i.org_id = $1 AND i.solution_identifier = $2 AND i.status = 'revoked'
		  AND n.kind = 'solution'
		ORDER BY i.revoked_at DESC
		LIMIT 1`,
		params.OrgID, params.SolutionIdentifier).Scan(&nodeID, &nodePath)
	if err == nil {
		return nodeID, nodePath, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", w.Wrapf(err, "failed to look up prior solution scope node")
	}
	label := params.RootScopeLabel
	if label == "" {
		label = params.SolutionIdentifier
	}
	node := &gen.ScopeNode{
		Id:    business.NewIDString(),
		OrgId: params.OrgID,
		Kind:  "solution",
		Label: label,
	}
	node.ScopePath = strings.ReplaceAll(node.Id, "-", "_")
	if err := s.RegisterScopeNode(ctx, node); err != nil {
		return "", "", w.Wrapf(err, "failed to register solution scope node")
	}
	return node.Id, node.ScopePath, nil
}

// reconcileExistingInstallation guards the idempotent re-install path: it returns
// a conflict when a re-install of an already-active solution names a different
// authority envelope (ceiling, standing-grant role, or co-owner set) than the one
// on record, so a narrowed ceiling is never silently discarded. Identical params
// reconcile to a no-op and the caller returns the existing row.
func reconcileExistingInstallation(ctx context.Context, executor QueryExecutor, existing *gen.Installation, params *business.InstallSolutionParams) error {
	var allowedAudiences, allowedScopes []string
	if err := executor.QueryRow(ctx,
		`SELECT allowed_audiences, allowed_scopes FROM principals WHERE id = $1 AND org_id = $2`,
		existing.AgentPrincipalId, existing.OrgId).Scan(&allowedAudiences, &allowedScopes); err != nil {
		return fmt.Errorf("load existing agent ceiling: %w", err)
	}
	// A missing standing grant (an admin revoked it out from under an active
	// install) leaves roleID empty, which mismatches the requested role below and
	// fails closed to a conflict rather than an opaque Internal from ErrNoRows.
	var roleID string
	if err := executor.QueryRow(ctx, `
		SELECT g.role_id::text
		FROM scope_grants g
		JOIN scope_nodes n ON n.id = $2
		WHERE g.org_id = $1 AND g.subject_kind = 'principal'
		  AND g.subject_id = $3 AND g.scope_path = n.scope_path
		LIMIT 1`,
		existing.OrgId, existing.RootScopeNodeId, existing.AgentPrincipalId).Scan(&roleID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load existing standing grant role: %w", err)
	}
	if roleID == params.RoleID &&
		sameStringSet(allowedAudiences, params.AllowedAudiences) &&
		sameStringSet(allowedScopes, params.AllowedScopes) &&
		sameStringSet(existing.CoOwnerPrincipalIds, params.CoOwnerPrincipalIDs) {
		return nil
	}
	return business.NewStoreError(
		fmt.Errorf("solution %s is already installed with a different ceiling, role, or co-owner set; uninstall and reinstall to change it",
			params.SolutionIdentifier),
		business.ErrTypeConflict)
}

// sameStringSet reports whether two string slices hold the same values regardless
// of order. A nil and an empty slice are equal (both mean "unrestricted" for a
// ceiling).
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sortedA := append([]string(nil), a...)
	sortedB := append([]string(nil), b...)
	slices.Sort(sortedA)
	slices.Sort(sortedB)
	return slices.Equal(sortedA, sortedB)
}

// GetInstallation loads one installation and resolves its health live from the
// current agent lifecycle, owner admin status, and standing grant.
func (s *PostgresStore) GetInstallation(ctx context.Context, orgID, installationID string) (*gen.Installation, gen.InstallationHealth, error) {
	w := wool.Get(ctx).In("GetInstallation", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	installation, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations WHERE id = $1 AND org_id = $2`,
		installationID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED,
				business.NewStoreError(fmt.Errorf("installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, w.Wrapf(err, "failed to get installation")
	}

	health, err := s.resolveInstallationHealth(ctx, executor, installation)
	if err != nil {
		return nil, gen.InstallationHealth_INSTALLATION_HEALTH_UNSPECIFIED, err
	}
	return installation, health, nil
}

func (s *PostgresStore) resolveInstallationHealth(ctx context.Context, executor QueryExecutor, installation *gen.Installation) (gen.InstallationHealth, error) {
	var revoked, disabled bool
	err := executor.QueryRow(ctx, `
		SELECT revoked_at IS NOT NULL, disabled_at IS NOT NULL
		FROM principals WHERE id = $1 AND org_id = $2 AND kind = 'agent'`,
		installation.AgentPrincipalId, installation.OrgId).Scan(&revoked, &disabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED, nil
		}
		return 0, fmt.Errorf("resolve installation agent health: %w", err)
	}
	switch {
	case revoked:
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_REVOKED, nil
	case disabled:
		return gen.InstallationHealth_INSTALLATION_HEALTH_AGENT_DISABLED, nil
	}

	owner, err := firstEligibleOwner(ctx, executor, installation.OrgId,
		append([]string{installation.OwnerPrincipalId}, installation.CoOwnerPrincipalIds...))
	if err != nil {
		return 0, fmt.Errorf("resolve installation owner health: %w", err)
	}
	if owner == "" {
		return gen.InstallationHealth_INSTALLATION_HEALTH_NO_ELIGIBLE_OWNER, nil
	}

	var hasGrant bool
	if err := executor.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM scope_grants g
			JOIN scope_nodes n ON n.id = $3
			WHERE g.org_id = $1
			  AND g.subject_kind = 'principal'
			  AND g.subject_id = $2
			  AND g.scope_path @> n.scope_path
			  AND (g.expires_at IS NULL OR g.expires_at > now())
		)`,
		installation.OrgId, installation.AgentPrincipalId, installation.RootScopeNodeId,
	).Scan(&hasGrant); err != nil {
		return 0, fmt.Errorf("resolve installation grant health: %w", err)
	}
	if !hasGrant {
		return gen.InstallationHealth_INSTALLATION_HEALTH_STANDING_GRANT_MISSING, nil
	}
	return gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY, nil
}

// TransferInstallationOwnership reassigns the owner of record and replaces the
// co-owner set. The new owner must be a current org admin.
func (s *PostgresStore) TransferInstallationOwnership(ctx context.Context, orgID, installationID, newOwnerPrincipalID string, coOwnerPrincipalIDs []string) (*gen.Installation, error) {
	w := wool.Get(ctx).In("TransferInstallationOwnership", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	admin, err := isCurrentOrgAdmin(ctx, executor, orgID, newOwnerPrincipalID)
	if err != nil {
		return nil, w.Wrapf(err, "failed to check new owner org-admin status")
	}
	if !admin {
		return nil, business.NewStoreError(
			fmt.Errorf("new owner %s is not a current org admin", newOwnerPrincipalID),
			business.ErrTypeConflict,
		)
	}

	installation, err := scanInstallation(executor.QueryRow(ctx, `
		UPDATE installations
		SET owner_principal_id = $3, co_owner_principal_ids = $4
		WHERE id = $1 AND org_id = $2 AND status = 'active'
		RETURNING `+installationColumns,
		installationID, orgID, newOwnerPrincipalID, stringArray(coOwnerPrincipalIDs),
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				fmt.Errorf("active installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, w.Wrapf(err, "failed to transfer installation ownership")
	}
	return installation, nil
}

// UninstallSolution soft-deletes the installation and reverses the composition:
// it removes the agent's standing grant and revokes the agent principal. The
// solution scope node is left in place (inert without a grant or a live agent) so
// a reinstall reuses it. Idempotent on an already-revoked installation.
func (s *PostgresStore) UninstallSolution(ctx context.Context, orgID, installationID string) (*gen.Installation, bool, error) {
	w := wool.Get(ctx).In("UninstallSolution", wool.Field("installation_id", installationID))
	executor := s.getQueryExecutor(ctx)

	current, err := scanInstallation(executor.QueryRow(ctx,
		`SELECT `+installationColumns+` FROM installations WHERE id = $1 AND org_id = $2`,
		installationID, orgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, business.NewStoreError(
				fmt.Errorf("installation %s not found", installationID), business.ErrTypeNotFound)
		}
		return nil, false, w.Wrapf(err, "failed to load installation")
	}
	if current.Status == gen.InstallationStatus_INSTALLATION_STATUS_REVOKED {
		return current, false, nil
	}

	// Remove the agent's standing grant, then revoke the agent principal. The
	// solution scope node is left in place: the soft-deleted installation row
	// still references it, and with no grant and a revoked agent the node is inert
	// (CheckAccess resolves nothing through it). Order is arbitrary within the tx.
	if _, err := executor.Exec(ctx, `
		DELETE FROM scope_grants
		WHERE org_id = $1 AND subject_kind = 'principal' AND subject_id = $2
		  AND scope_path = (SELECT scope_path FROM scope_nodes WHERE id = $3)`,
		orgID, current.AgentPrincipalId, current.RootScopeNodeId,
	); err != nil {
		return nil, false, w.Wrapf(err, "failed to revoke standing grant")
	}
	if _, err := executor.Exec(ctx, `
		UPDATE principals SET revoked_at = CURRENT_TIMESTAMP, revoked_reason = 'installation uninstalled'
		WHERE id = $1 AND org_id = $2 AND kind = 'agent' AND revoked_at IS NULL`,
		current.AgentPrincipalId, orgID,
	); err != nil {
		return nil, false, w.Wrapf(err, "failed to revoke agent principal")
	}

	revoked, err := scanInstallation(executor.QueryRow(ctx, `
		UPDATE installations SET status = 'revoked', revoked_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND org_id = $2 AND status = 'active'
		RETURNING `+installationColumns,
		installationID, orgID,
	))
	if err != nil {
		return nil, false, w.Wrapf(err, "failed to mark installation revoked")
	}
	return revoked, true, nil
}

// ResolveInstallationAuthority is the headless-mint authority resolution. It runs
// under the control plane because the caller is a service credential with no user
// identity; every query is explicitly org-scoped, so cross-tenant reach is
// impossible despite the bypassed RLS floor.
func (s *PostgresStore) ResolveInstallationAuthority(ctx context.Context, orgID, installationID string, permissions []business.WorkContextPermission) (*business.InstallationAuthorityFacts, error) {
	var facts *business.InstallationAuthorityFacts
	err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		var resolveErr error
		facts, resolveErr = resolveInstallationAuthority(ctx, s.getQueryExecutor(ctx), orgID, installationID, permissions)
		return resolveErr
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
}

func resolveInstallationAuthority(ctx context.Context, executor QueryExecutor, orgID, installationID string, permissions []business.WorkContextPermission) (*business.InstallationAuthorityFacts, error) {
	var (
		agentID, ownerID, rootNodeID, rootScopePath string
		coOwners                                    []string
	)
	err := executor.QueryRow(ctx, `
		SELECT i.agent_principal_id::text, i.owner_principal_id::text,
		       i.co_owner_principal_ids::text[], i.root_scope_node_id::text, n.scope_path::text
		FROM installations i
		JOIN scope_nodes n ON n.id = i.root_scope_node_id
		WHERE i.id = $1 AND i.org_id = $2 AND i.status = 'active'`,
		installationID, orgID,
	).Scan(&agentID, &ownerID, &coOwners, &rootNodeID, &rootScopePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(
			fmt.Errorf("active installation %s not found", installationID), business.ErrTypeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve installation: %w", err)
	}

	// Agent actor, fail-closed on revoke/disable — the same filter the delegated
	// mint applies in resolveWorkContextAuthority.
	var actor business.Principal
	var orgIDOut, agentIdentifier, createdBy *string
	err = executor.QueryRow(ctx, `
		SELECT id::text, kind, display_name, org_id::text, agent_identifier,
		       created_at, created_by::text, allowed_audiences, allowed_scopes
		FROM principals
		WHERE id = $1 AND org_id = $2 AND kind = 'agent'
		  AND revoked_at IS NULL AND disabled_at IS NULL`,
		agentID, orgID,
	).Scan(&actor.ID, &actor.Kind, &actor.DisplayName, &orgIDOut, &agentIdentifier,
		&actor.CreatedAt, &createdBy, &actor.AllowedAudiences, &actor.AllowedScopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(
			errors.New("installation agent principal is revoked or disabled"), business.ErrTypeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve installation actor: %w", err)
	}
	if orgIDOut != nil {
		actor.OrgID = *orgIDOut
	}
	if agentIdentifier != nil {
		actor.AgentIdentifier = *agentIdentifier
	}
	if createdBy != nil {
		actor.CreatedBy = *createdBy
	}

	// Owner of record, resolved live and fail-closed: none of the declared
	// owner/co-owners is currently an org admin.
	ownerOfRecord, err := firstEligibleOwner(ctx, executor, orgID,
		append([]string{ownerID}, coOwners...))
	if err != nil {
		return nil, fmt.Errorf("resolve installation owner of record: %w", err)
	}
	if ownerOfRecord == "" {
		return nil, business.NewStoreError(
			errors.New("no owner or co-owner of the installation is currently an org admin"),
			business.ErrTypeNotFound)
	}

	facts := &business.InstallationAuthorityFacts{OwnerPrincipalID: ownerOfRecord, Actor: &actor}
	var orgRevision, ownerRevision int64
	var teamIDs []string
	err = executor.QueryRow(ctx, `
		SELECT organization_revision.revision,
		       principal_revision.revision,
		       COALESCE(
		           array_agg(team.id::text ORDER BY team.id)
		               FILTER (WHERE team.id IS NOT NULL),
		           ARRAY[]::text[]
		       )
		FROM organization_authorization_revisions AS organization_revision
		JOIN principal_authorization_revisions AS principal_revision
		  ON principal_revision.org_id = organization_revision.org_id
		 AND principal_revision.principal_id = $2
		LEFT JOIN team_members AS team_membership
		  ON team_membership.user_id = $2
		LEFT JOIN teams AS team
		  ON team.id = team_membership.team_id
		 AND team.org_id = organization_revision.org_id
		WHERE organization_revision.org_id = $1
		GROUP BY organization_revision.revision, principal_revision.revision`,
		orgID, ownerOfRecord,
	).Scan(&orgRevision, &ownerRevision, &teamIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve installation owner revision: %w", err)
	}
	if orgRevision <= 0 || ownerRevision <= 0 {
		return nil, errors.New("authorization revision must be positive")
	}
	facts.OrganizationRevision = uint64(orgRevision)
	facts.OwnerPrincipalRevision = uint64(ownerRevision)
	facts.AttributionTeamIDs = append([]string(nil), teamIDs...)

	// Every requested scope must fall within the agent's STANDING grant, resolved
	// hierarchically against the scope tree. A scoped request names a boundary node
	// (resource_id); an unscoped one is checked against the installation root.
	for _, permission := range permissions {
		allowed, err := installationScopeAllowed(ctx, executor, orgID, agentID, rootScopePath, permission)
		if err != nil {
			return nil, fmt.Errorf("check installation standing grant: %w", err)
		}
		if !allowed {
			return nil, business.NewStoreError(
				fmt.Errorf("agent standing grant does not authorize %s:%s at requested scope",
					permission.ResourceKind, permission.Action),
				business.ErrTypePermission)
		}
	}
	return facts, nil
}

// installationScopeAllowed resolves whether the agent's standing scope grant
// authorizes one requested (kind, action) at a boundary. The boundary's scope
// path is resolved from its own registered node (resource_id) — never trusted
// from the request — and must be a descendant-or-equal of the installation's own
// root; the grant must then sit at an ancestor-or-equal of the boundary, with a
// role permitting the (kind, action). Bounding the boundary by the root is what
// keeps a grant the agent later receives elsewhere (a record share, another
// install's node, an admin's ad-hoc GrantScope) from widening what this headless
// path may mint for. An unscoped request (empty ResourceID) resolves against the
// installation's own root path.
func installationScopeAllowed(ctx context.Context, executor QueryExecutor, orgID, agentID, rootScopePath string, permission business.WorkContextPermission) (bool, error) {
	var allowed bool
	err := executor.QueryRow(ctx, `
		WITH boundary AS (
			SELECT CASE
				WHEN $5 = '' THEN $6::ltree
				ELSE (SELECT scope_path FROM scope_nodes WHERE org_id = $1 AND id::text = $5)
			END AS scope_path
		)
		SELECT EXISTS (
			SELECT 1
			FROM scope_grants g
			JOIN role_permissions rp ON rp.role_id = g.role_id
			CROSS JOIN boundary
			WHERE g.org_id = $1
			  AND g.subject_kind = 'principal'
			  AND g.subject_id = $2
			  AND boundary.scope_path IS NOT NULL
			  AND boundary.scope_path <@ $6::ltree
			  AND g.scope_path @> boundary.scope_path
			  AND (g.expires_at IS NULL OR g.expires_at > now())
			  AND (rp.resource = '*' OR rp.resource = $3)
			  AND (rp.action = '*' OR rp.action = $4)
		)`,
		orgID, agentID, permission.ResourceKind, permission.Action, permission.ResourceID, rootScopePath,
	).Scan(&allowed)
	return allowed, err
}

var _ business.InstallationStore = (*PostgresStore)(nil)
