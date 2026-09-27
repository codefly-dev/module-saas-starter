package infra

import (
	"context"
	"fmt"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
	"github.com/google/uuid"
)

// ListSolutionEntitlements answers what one subject may use inside one org
// (issue #949): the org's active installations, narrowed to those whose
// authority-root scope node the subject may act on.
//
// It is ONE statement on purpose. The installation set and the grant + share
// union are two authority facts, and reading them in two round trips lets a
// revoke land between them — producing a menu entry whose grant is already gone,
// and a fingerprint describing a state that never existed. Joined here, both are
// read at one snapshot inside the caller's org transaction.
//
// The admitted-scope subquery is `accessibleScopesQuery`, the same union
// CheckAccess and ListAccessibleScopes resolve through — not a second predicate
// that means to agree with it. That is what makes it impossible for this listing
// to offer a solution an authority check would then deny.
//
// The join is on `n.id::text = root_scope_node_id::text`, the projection the scope
// listing itself returns as node_id, for the reason CanReadScopeNode compares
// there: it keeps membership and listing reading one set by construction.
func (s *PostgresStore) ListSolutionEntitlements(
	ctx context.Context, orgID, subjectID string, pageToken string, limit int,
) ([]*gen.SolutionEntitlement, error) {
	w := wool.Get(ctx).In("ListSolutionEntitlements", wool.Field("org_id", orgID))
	executor := s.getQueryExecutor(ctx)

	cursor := ""
	if pageToken != "" {
		parsed, err := uuid.Parse(pageToken)
		if err != nil {
			return nil, business.NewStoreError(
				fmt.Errorf("page_token %q is not a cursor this listing issued", pageToken),
				business.ErrTypeValidation,
			)
		}
		cursor = parsed.String()
	}

	// $1 subject, $2 resource_type, $3 action, $4 org, $5 the node kind this
	// narrows to, $6 platform admissibility — the parameter contract
	// accessibleScopesQuery documents — then $7 cursor and $8 limit, appended by
	// this query.
	//
	// The node predicate is the node KIND rather than a candidate id set: a
	// solution's authority root is the one node per install of kind `solution`, so
	// asking the union for accessible solution nodes is the question, and it needs
	// no array of ids assembled by a prior query.
	admitted, err := accessibleScopesQuery(
		gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "node_id", "n.kind = $5", "$6")
	if err != nil {
		return nil, err
	}

	// Only ACTIVE installations. A revoked installation's standing grant is gone,
	// so the union would usually exclude it anyway — but "usually" is not a
	// boundary: a team grant written directly at the solution node outlives the
	// uninstall (the node is retained for a reinstall to reuse), and without this
	// predicate an uninstalled solution would stay in a granted team's menu.
	rows, err := executor.Query(ctx,
		`SELECT `+installationColumns+`
		 FROM installations
		 WHERE org_id = $4
		   AND status = 'active'
		   AND root_scope_node_id::text IN (`+admitted+`)
		   AND ($7 = '' OR id::text > $7)
		 ORDER BY id
		 LIMIT $8`,
		subjectID, business.ResourceTypeSolution, business.ActionUseSolution, orgID,
		business.ScopeNodeKindSolution,
		platformReadAdmissible(ctx, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, business.ActionUseSolution),
		cursor, limit)
	if err != nil {
		return nil, w.Wrapf(err, "failed to list solution entitlements")
	}
	defer rows.Close()

	var installations []*gen.Installation
	for rows.Next() {
		installation, e := scanInstallation(rows)
		if e != nil {
			return nil, w.Wrapf(e, "failed to scan entitled installation")
		}
		installations = append(installations, installation)
	}
	if e := rows.Err(); e != nil {
		return nil, w.Wrapf(e, "failed to read solution entitlements")
	}

	// Health resolution issues its own queries, so it runs after this cursor is
	// drained rather than inside the loop.
	out := make([]*gen.SolutionEntitlement, 0, len(installations))
	for _, installation := range installations {
		health, e := s.resolveInstallationHealth(ctx, executor, installation)
		if e != nil {
			return nil, w.Wrapf(e, "failed to resolve entitled installation health")
		}
		out = append(out, &gen.SolutionEntitlement{
			SolutionIdentifier: installation.GetSolutionIdentifier(),
			InstallationId:     installation.GetId(),
			RootScopeNodeId:    installation.GetRootScopeNodeId(),
			// Anything that is not HEALTHY is not healthy: the reduction survives
			// the health enum growing, which a copy of its value set would not.
			Healthy: health == gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		})
	}
	return out, nil
}

var _ business.SolutionEntitlementStore = (*PostgresStore)(nil)
