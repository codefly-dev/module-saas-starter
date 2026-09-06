package infra

import (
	"context"
	"errors"
	"fmt"

	"accounts/pkg/business"

	"github.com/jackc/pgx/v5"
)

// ResolveWorkContextAuthority resolves current membership, monotonic
// authorization revisions, immutable team attribution, an optional registered
// agent actor, and every requested scope in one service-postgres Reader
// transaction. tenant/user are checked against the private identity installed
// by the authentication interceptor before the Reader is issued.
func (s *PostgresStore) ResolveWorkContextAuthority(
	ctx context.Context,
	orgID string,
	ownerPrincipalID string,
	actorPrincipalID string,
	permissions []business.WorkContextPermission,
) (*business.WorkContextAuthorityFacts, error) {
	var facts *business.WorkContextAuthorityFacts
	err := s.readAs(ctx, orgID, ownerPrincipalID, func(ctx context.Context, reader ReadQueryExecutor) error {
		var resolveErr error
		facts, resolveErr = resolveWorkContextAuthority(
			ctx,
			reader,
			orgID,
			ownerPrincipalID,
			actorPrincipalID,
			permissions,
			// The mint path resolves flat RBAC only; installation authority is
			// resolved by the installation store, not here.
			false,
		)
		return resolveErr
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
}

// includeScopeGrants widens each permission check to also accept a hierarchical
// scope_grant (the installation authority store), not only a flat role_assignment.
// It is the consumer-revalidation seam's counterpart to the installation mint:
// StartInstallationTask draws an agent's authority from scope_grants, so
// CheckWorkContextAuthorizationRevision must resolve it the same way or it would
// reject every validly-minted installation token as stale. It stays OFF for the
// mint paths so delegated (owner ∩ actor via RBAC) minting is unchanged.
func resolveWorkContextAuthority(
	ctx context.Context,
	reader ReadQueryExecutor,
	orgID string,
	ownerPrincipalID string,
	actorPrincipalID string,
	permissions []business.WorkContextPermission,
	includeScopeGrants bool,
) (*business.WorkContextAuthorityFacts, error) {
	facts := &business.WorkContextAuthorityFacts{}
	var orgRevision int64
	var principalRevision int64
	var teamIDs []string
	err := reader.QueryRow(ctx, `
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
			JOIN organization_members AS membership
			  ON membership.org_id = organization_revision.org_id
			 AND membership.user_id = $2
			LEFT JOIN team_members AS team_membership
			  ON team_membership.user_id = membership.user_id
			LEFT JOIN teams AS team
			  ON team.id = team_membership.team_id
			 AND team.org_id = membership.org_id
			WHERE organization_revision.org_id = $1
			GROUP BY organization_revision.revision, principal_revision.revision`,
		orgID, ownerPrincipalID,
	).Scan(&orgRevision, &principalRevision, &teamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, business.NewStoreError(
			errors.New("current organization membership and authorization revision required"),
			business.ErrTypeNotFound,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve work-context owner facts: %w", err)
	}
	if orgRevision <= 0 || principalRevision <= 0 {
		return nil, errors.New("authorization revision must be positive")
	}
	facts.OrganizationRevision = uint64(orgRevision)
	facts.PrincipalRevision = uint64(principalRevision)
	facts.AttributionTeamIDs = append([]string(nil), teamIDs...)

	if actorPrincipalID != "" {
		var actor business.Principal
		var orgIDOut, agentIdentifier, createdBy *string
		err := reader.QueryRow(ctx, `
				SELECT id, kind, display_name, org_id, agent_identifier,
				       created_at, created_by, allowed_audiences, allowed_scopes
				FROM principals
				WHERE id = $2
				  AND org_id = $1
				  AND kind = 'agent'
				  AND revoked_at IS NULL
				  AND disabled_at IS NULL`,
			orgID, actorPrincipalID,
		).Scan(
			&actor.ID,
			&actor.Kind,
			&actor.DisplayName,
			&orgIDOut,
			&agentIdentifier,
			&actor.CreatedAt,
			&createdBy,
			&actor.AllowedAudiences,
			&actor.AllowedScopes,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, business.NewStoreError(
				errors.New("active registered agent principal required"),
				business.ErrTypeNotFound,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve work-context actor: %w", err)
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
		facts.Actor = &actor
	}

	for _, permission := range permissions {
		ownerAllowed, err := workContextPermissionAllowed(
			ctx, reader, orgID, ownerPrincipalID, true, permission, includeScopeGrants,
		)
		if err != nil {
			return nil, fmt.Errorf("check owner work-context permission: %w", err)
		}
		if !ownerAllowed {
			return nil, business.NewStoreError(
				fmt.Errorf(
					"owner is not allowed %s:%s at requested scope",
					permission.ResourceKind,
					permission.Action,
				),
				business.ErrTypePermission,
			)
		}
		if actorPrincipalID == "" {
			continue
		}
		actorAllowed, err := workContextPermissionAllowed(
			ctx, reader, orgID, actorPrincipalID, false, permission, includeScopeGrants,
		)
		if err != nil {
			return nil, fmt.Errorf("check actor work-context permission: %w", err)
		}
		if !actorAllowed {
			return nil, business.NewStoreError(
				fmt.Errorf(
					"actor is not allowed %s:%s at requested scope",
					permission.ResourceKind,
					permission.Action,
				),
				business.ErrTypePermission,
			)
		}
	}
	return facts, nil
}

func (s *PostgresStore) CheckWorkContextAuthorizationRevision(
	ctx context.Context,
	orgID string,
	ownerPrincipalID string,
	expectedRevision uint64,
	subjects []business.WorkContextRevisionSubject,
) error {
	return s.readAs(ctx, orgID, ownerPrincipalID, func(ctx context.Context, reader ReadQueryExecutor) error {
		for _, subject := range subjects {
			actorID := subject.PrincipalID
			if actorID == ownerPrincipalID {
				actorID = ""
			}
			facts, err := resolveWorkContextAuthority(
				ctx,
				reader,
				orgID,
				ownerPrincipalID,
				actorID,
				subject.Permissions,
				// A previously issued Work Context may be installation-derived, whose
				// actor authority lives in scope_grants; honor it here so a valid
				// headless token is not rejected as stale.
				true,
			)
			if err != nil {
				var storeErr *business.StoreError
				if errors.As(err, &storeErr) &&
					(storeErr.StoreErrorType == business.ErrTypeNotFound ||
						storeErr.StoreErrorType == business.ErrTypePermission) {
					return fmt.Errorf("%w: %v", business.ErrWorkContextAuthorizationStale, err)
				}
				return err
			}
			if facts.EffectiveRevision() != expectedRevision {
				return business.ErrWorkContextAuthorizationStale
			}
		}
		return nil
	})
}

func (s *PostgresStore) AuthorizeEvidenceRead(
	ctx context.Context,
	orgID string,
	callerPrincipalID string,
	ownerPrincipalID string,
	taskID string,
	_ string,
) error {
	return s.readAs(ctx, orgID, callerPrincipalID, func(ctx context.Context, reader ReadQueryExecutor) error {
		var member bool
		if err := reader.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM organization_members
				WHERE org_id = $1
				  AND user_id = $2
			)`,
			orgID,
			callerPrincipalID,
		).Scan(&member); err != nil {
			return fmt.Errorf("check Evidence reader membership: %w", err)
		}
		if !member {
			return business.ErrEvidenceReadDenied
		}
		// A current member may read their own Evidence directory, optionally
		// narrowed by Task or Session, without acquiring a cross-owner grant.
		if ownerPrincipalID != "" && callerPrincipalID == ownerPrincipalID {
			return nil
		}
		// A Task filter can be decided by an exact resource-scoped grant. Any
		// broader filter requires an unscoped assignment; passing an empty
		// ResourceID deliberately excludes resource-scoped assignments.
		resourceID := ""
		if taskID != "" {
			resourceID = taskID
		}
		allowed, err := workContextPermissionAllowed(
			ctx,
			reader,
			orgID,
			callerPrincipalID,
			true,
			business.WorkContextPermission{
				ResourceKind: "evidence",
				Action:       "read",
				ResourceID:   resourceID,
			},
			false,
		)
		if err != nil {
			return fmt.Errorf("check Evidence read permission: %w", err)
		}
		if !allowed {
			return business.ErrEvidenceReadDenied
		}
		return nil
	})
}

func workContextPermissionAllowed(
	ctx context.Context,
	reader ReadQueryExecutor,
	orgID string,
	principalID string,
	includeTeamsAndOrgAdministration bool,
	permission business.WorkContextPermission,
	includeScopeGrants bool,
) (bool, error) {
	var allowed bool
	err := reader.QueryRow(ctx, `
		SELECT
		    (
		        $5
		        AND EXISTS (
		            SELECT 1
		            FROM organization_members AS administrator
		            WHERE administrator.org_id = $1
		              AND administrator.user_id = $2
		              AND administrator.role IN ('owner', 'admin')
		        )
		    )
		    OR EXISTS (
		        SELECT 1
		        FROM role_assignments AS assignment
		        JOIN roles AS role
		          ON role.id = assignment.role_id
		        JOIN role_permissions AS granted
		          ON granted.role_id = role.id
		        WHERE assignment.org_id = $1
		          AND (
		              (
		                  assignment.subject_kind = 'principal'
		                  AND assignment.subject_id = $2
		              )
		              OR (
		                  $5
		                  AND assignment.subject_kind = 'team'
		                  AND assignment.subject_id IN (
		                      SELECT team_id
		                      FROM team_members
		                      WHERE user_id = $2
		                  )
		              )
		          )
		          AND (granted.resource = '*' OR granted.resource = $3)
		          AND (granted.action = '*' OR granted.action = $4)
		          AND (
		              ($6 = '' AND assignment.scope IS NULL)
		              OR ($6 <> '' AND (assignment.scope IS NULL OR assignment.scope = $6))
		          )
		    )
		    -- Installation authority: a hierarchical scope_grant at an ancestor-or-
		    -- equal of the boundary node named by resource_id (never trusted from the
		    -- request — resolved from the node's own row), with a role permitting the
		    -- (kind, action). Only the recheck seam sets $7; an empty resource_id
		    -- matches no node, so an unscoped permission never resolves here.
		    OR (
		        $7
		        AND EXISTS (
		            SELECT 1
		            FROM scope_grants AS sg
		            JOIN role_permissions AS sg_permission
		              ON sg_permission.role_id = sg.role_id
		            JOIN scope_nodes AS boundary
		              ON boundary.org_id = $1
		             AND boundary.id::text = $6
		            WHERE sg.org_id = $1
		              AND sg.subject_kind = 'principal'
		              AND sg.subject_id = $2
		              AND sg.scope_path @> boundary.scope_path
		              AND (sg.expires_at IS NULL OR sg.expires_at > now())
		              AND (sg_permission.resource = '*' OR sg_permission.resource = $3)
		              AND (sg_permission.action = '*' OR sg_permission.action = $4)
		        )
		    )`,
		orgID,
		principalID,
		permission.ResourceKind,
		permission.Action,
		includeTeamsAndOrgAdministration,
		permission.ResourceID,
		includeScopeGrants,
	).Scan(&allowed)
	return allowed, err
}

var _ business.WorkContextAuthorityStore = (*PostgresStore)(nil)
var _ business.WorkContextConsumerAuthorityStore = (*PostgresStore)(nil)
