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
	// accessibleScopesQuery documents. The shared listing numbers its own
	// parameters after these.
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
	//
	// Through the SAME listing ListInstallations reads, narrowed by the admitted
	// set, so the two reads share one query, one keyset and one health definition,
	// and health for every row is resolved inside this one statement.
	summaries, err := s.listInstallationSummaries(ctx, executor, orgID, "active", cursor, limit,
		"i.root_scope_node_id::text IN ("+admitted+")",
		subjectID, business.ResourceTypeSolution, business.ActionUseSolution, orgID,
		business.ScopeNodeKindSolution,
		platformReadAdmissible(ctx, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, business.ActionUseSolution))
	if err != nil {
		return nil, w.Wrapf(err, "failed to list solution entitlements")
	}

	out := make([]*gen.SolutionEntitlement, 0, len(summaries))
	for _, summary := range summaries {
		installation := summary.GetInstallation()
		out = append(out, &gen.SolutionEntitlement{
			// The immutable target, never the route alias: a consumer resolves
			// the alias it was asked about through applied presence state and
			// compares this id, so a replacement binding that claimed a
			// withdrawn alias cannot match an installation of its predecessor.
			TargetId:        installation.GetTargetId(),
			InstallationId:  installation.GetId(),
			RootScopeNodeId: installation.GetRootScopeNodeId(),
			// Anything that is not HEALTHY is not healthy: the reduction survives
			// the health enum growing, which a copy of its value set would not.
			Healthy: summary.GetHealth() == gen.InstallationHealth_INSTALLATION_HEALTH_HEALTHY,
		})
	}
	return out, nil
}

var _ business.SolutionEntitlementStore = (*PostgresStore)(nil)
