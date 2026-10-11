package business

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// SolutionEntitlementStore is the narrow persistence surface behind the
// per-viewer solution projection (issue #949), mirroring the InstallationStore
// pattern so business depends on an interface rather than the full PostgresStore.
// Its one method runs inside the caller's WithOrgTx transaction.
type SolutionEntitlementStore interface {
	// ListSolutionEntitlements returns the org's active installations whose
	// authority-root scope node subjectID may act on, ordered on the keyset the
	// cursor advances. limit is the wanted page size PLUS one: the extra row is how
	// the caller detects a further page, as in ListAccessibleScopes.
	ListSolutionEntitlements(ctx context.Context, orgID, subjectID string, pageToken string, limit int) ([]*gen.SolutionEntitlement, error)
}

func (s *Service) solutionEntitlementStore() SolutionEntitlementStore {
	if es, ok := s.store.(SolutionEntitlementStore); ok {
		return es
	}
	panic("Service.store does not implement SolutionEntitlementStore; see postgres_solution_entitlements.go")
}

// listSolutionEntitlementsDefaultPageSize / …MaxPageSize bound the listing; the
// max mirrors the proto ceiling. The default is the same as the installation
// listing's, because this is that listing narrowed by authority and an
// organization's installed set is what both page over.
const (
	listSolutionEntitlementsDefaultPageSize = 200
	listSolutionEntitlementsMaxPageSize     = 500
)

// ListSolutionEntitlements enumerates the installed solutions a subject may use.
// Always org-scoped, so it runs under WithOrgTx and RLS confines it to that
// tenant; the store resolves the installation set and the same grant + share
// union CheckAccess resolves in one statement, so the projection can never offer
// what an authority check would deny.
func (s *Service) ListSolutionEntitlements(ctx context.Context, req *gen.ListSolutionEntitlementsRequest) (*gen.ListSolutionEntitlementsResponse, error) {
	w := wool.Get(ctx).In("ListSolutionEntitlements",
		wool.Field("org_id", req.GetOrgId()), wool.Field("subject_id", req.GetSubjectId()))
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = listSolutionEntitlementsDefaultPageSize
	}
	if pageSize > listSolutionEntitlementsMaxPageSize {
		pageSize = listSolutionEntitlementsMaxPageSize
	}

	var entitlements []*gen.SolutionEntitlement
	if err := s.store.WithOrgTx(ctx, req.GetOrgId(), func(ctx context.Context) error {
		out, e := s.solutionEntitlementStore().ListSolutionEntitlements(
			ctx, req.GetOrgId(), req.GetSubjectId(), req.GetPageToken(), pageSize+1)
		entitlements = out
		return e
	}); err != nil {
		return nil, w.Wrapf(err, "cannot list solution entitlements")
	}

	var nextToken string
	if len(entitlements) > pageSize {
		entitlements = entitlements[:pageSize]
		// The cursor is the last RETURNED row's key, so the next page resumes at
		// the row after the one the caller saw.
		nextToken = entitlements[pageSize-1].GetInstallationId()
	}
	return &gen.ListSolutionEntitlementsResponse{Entitlements: entitlements, NextPageToken: nextToken}, nil
}
