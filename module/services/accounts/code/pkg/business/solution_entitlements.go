package business

import (
	"context"
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/wool"
)

// SolutionEntitlementStore is the narrow persistence surface behind the
// per-viewer solution projection (issue #949), mirroring the InstallationStore
// pattern so business depends on an interface rather than the full PostgresStore.
// Its one method runs inside the caller's WithOrgTx transaction.
type SolutionEntitlementStore interface {
	InstallationAuthorityState(context.Context, string, string) (*InstallationAuthorityState, error)
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
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return nil, fenceErr
	}
	defer releaseAuthority()

	w := wool.Get(ctx).In("ListSolutionEntitlements",
		wool.Field("org_id", req.GetOrgId()), wool.Field("subject_id", req.GetSubjectId()))
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = listSolutionEntitlementsDefaultPageSize
	}
	if pageSize > listSolutionEntitlementsMaxPageSize {
		pageSize = listSolutionEntitlementsMaxPageSize
	}

	snapshot := s.moduleRegistrySnapshot()
	if err := s.fenceModuleRegistrySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	var entitlements []*gen.SolutionEntitlement
	cursor := req.GetPageToken()
	for len(entitlements) <= pageSize {
		var rows []*gen.SolutionEntitlement
		states := map[string]*InstallationAuthorityState{}
		if err := s.store.WithOrgTx(ctx, req.GetOrgId(), func(ctx context.Context) error {
			var err error
			rows, err = s.solutionEntitlementStore().ListSolutionEntitlements(ctx, req.GetOrgId(), req.GetSubjectId(), cursor, pageSize+1)
			if err != nil {
				return err
			}
			for _, row := range rows {
				state, err := s.solutionEntitlementStore().InstallationAuthorityState(ctx, req.GetOrgId(), row.GetInstallationId())
				if err != nil {
					return err
				}
				states[row.GetInstallationId()] = state
			}
			return nil
		}); err != nil {
			return nil, w.Wrapf(err, "cannot list solution entitlements")
		}
		for _, row := range rows {
			allowed, err := s.installationEntitlementCurrent(ctx, snapshot, req.GetOrgId(), row.GetInstallationId(), states[row.GetInstallationId()])
			if err != nil {
				return nil, err
			}
			if allowed {
				entitlements = append(entitlements, row)
			}
			if len(entitlements) > pageSize {
				break
			}
		}
		if len(rows) < pageSize+1 {
			break
		}
		cursor = rows[len(rows)-1].GetInstallationId()
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

// Every proxied installation requires explicit registry-associated consent.
// Empty backfills cannot become module authority if a later declaration changes
// a target's routing kind. Re-consent must create the explicit association.
func (s *Service) installationEntitlementCurrent(ctx context.Context, snapshot *installedModuleRegistry, orgID, id string, state *InstallationAuthorityState) (bool, error) {
	ctx, releaseAuthority, fenceErr := s.BeginModuleAuthorityUse(ctx)
	if fenceErr != nil {
		return false, fenceErr
	}
	defer releaseAuthority()

	if state == nil {
		return false, nil
	}
	if state.PrincipalID == "" {
		return false, nil
	}
	authority, _, err := s.authorizeModuleCapabilityFromSnapshot(ctx, snapshot, ModuleCaller{PrincipalID: state.PrincipalID, BoundOrg: orgID})
	if err != nil {
		if status.Code(err) == codes.PermissionDenied || errors.Is(err, ErrModuleInstallationInactive) {
			return false, nil
		}
		return false, err
	}
	return authority.InstallationID == id && authority.InstallationRevision == state.Revision && authority.Grant.PresenceBinding == state.BindingID, nil
}
