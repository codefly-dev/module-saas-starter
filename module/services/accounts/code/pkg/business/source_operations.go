package business

import (
	"context"

	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SourceOperationStore interface {
	ReplaceSourceOperations(context.Context, string, string, []operations.Declaration) error
	ListSourceOperations(context.Context, string, string) ([]operations.Declaration, error)
}

// Declaration metadata is safe audit content: admitted identifiers only, no
// schemas, routes, input, output or provider messages.
func sourceDeclarationsAudit(declarations []operations.Declaration) map[string]any {
	names := make([]string, 0, len(declarations))
	for _, d := range declarations {
		names = append(names, d.Name)
	}
	return map[string]any{"operations": names}
}

// DeclareSourceOperations replaces a source's declared operations as an
// organization administrator. A member declaring on a personal source of their
// own goes through DeclareOwnSourceOperations instead.
func (s *Service) DeclareSourceOperations(ctx context.Context, actor, org, source string, declarations []operations.Declaration) ([]operations.Declaration, error) {
	return s.declareSourceOperations(ctx, actor, org, source, declarations, sourceDeclarer{
		member: func(membership *gen.OrgMembership) error {
			if membership == nil || !IsOrgAdminRole(orgRoleToString(membership.GetRole())) {
				return status.Error(codes.PermissionDenied, "organization administrator required")
			}
			return nil
		},
		source: func(_ context.Context, src *DatasourceSource) error {
			if src != nil && src.PersonalOwnerUserID != "" && src.PersonalOwnerUserID != actor {
				return status.Error(codes.PermissionDenied, "source is personal to another member")
			}
			return nil
		},
	})
}

// sourceDeclarer decides a declaration in two steps, so an administrator's
// check is made before the source is read and a caller who may not declare
// learns nothing of which sources exist: member on the caller's membership,
// then source on the source, nil when there is none.
type sourceDeclarer struct {
	member func(*gen.OrgMembership) error
	source func(context.Context, *DatasourceSource) error
}

// declareSourceOperations admits the set, then decides and replaces it in one
// organization transaction.
func (s *Service) declareSourceOperations(ctx context.Context, actor, org, source string, declarations []operations.Declaration, decide sourceDeclarer) ([]operations.Declaration, error) {
	if len(declarations) > 128 {
		return nil, status.Error(codes.InvalidArgument, "operation set exceeds bound")
	}
	admitted := make([]operations.Declaration, 0, len(declarations))
	names := map[string]bool{}
	for _, d := range declarations {
		checked, err := operations.Admit(d)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if names[checked.Name] {
			return nil, status.Error(codes.InvalidArgument, "duplicate operation name")
		}
		names[checked.Name] = true
		admitted = append(admitted, checked)
	}
	store, ok := s.store.(SourceOperationStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "source operations unavailable")
	}
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		membership, err := s.store.GetOrgMembership(ctx, org, actor)
		if err != nil {
			return status.Error(codes.Unavailable, "membership unavailable")
		}
		if err := decide.member(membership); err != nil {
			return err
		}
		src, err := s.store.GetDatasourceSource(ctx, org, source)
		if err != nil {
			return status.Error(codes.Unavailable, "source unavailable")
		}
		if err := decide.source(ctx, src); err != nil {
			return err
		}
		if src == nil {
			return status.Error(codes.NotFound, "source not found")
		}
		if src.Provider != DatasourceProviderAPI {
			return status.Error(codes.FailedPrecondition, "provider does not accept operations")
		}
		if err := store.ReplaceSourceOperations(ctx, org, source, admitted); err != nil {
			return status.Error(codes.Unavailable, "could not store operation set")
		}
		return s.emitTx(ctx, actor, "user", EventDatasourceOperationDeclared, "datasource", source, org, sourceDeclarationsAudit(admitted))
	})
	return admitted, err
}

func (s *Service) ListSourceOperations(ctx context.Context, holder SourceBoundaryHolder, actor, org, source string) ([]operations.Declaration, error) {
	store, ok := s.store.(SourceOperationStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "source operations unavailable")
	}
	var out []operations.Declaration
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		src, err := s.sourceOperationAccess(ctx, holder, actor, org, source, "read")
		if err != nil {
			return err
		}
		out, err = store.ListSourceOperations(ctx, org, src.ID)
		if err != nil {
			return status.Error(codes.Unavailable, "operation declarations unavailable")
		}
		return nil
	})
	return out, err
}

// This is the role/boundary layer, independent of the handler membership gate.
// Every invoke, lookup and discovery read re-evaluates it, including replays.
// actor is the person, whose source a personal one must be and whose
// membership is required; holder is whose grants decide on the source's
// boundary: the person, or the team a cast acts as and nothing of the
// person's own.
func (s *Service) sourceOperationAccess(ctx context.Context, holder SourceBoundaryHolder, actor, org, source, action string) (*DatasourceSource, error) {
	src, err := s.store.GetDatasourceSource(ctx, org, source)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "source unavailable")
	}
	if src == nil {
		return nil, status.Error(codes.NotFound, "source not found")
	}
	if src.PersonalOwnerUserID != "" && src.PersonalOwnerUserID != actor {
		return nil, status.Error(codes.PermissionDenied, "source is personal to another member")
	}
	membership, err := s.store.GetOrgMembership(ctx, org, actor)
	if err != nil || membership == nil {
		return nil, status.Error(codes.PermissionDenied, "organization membership required")
	}
	// A cast calls only a source of its person's own that no solution governs,
	// as the role-view gate admits it (castAsksForItsPersonsOwnSource): a
	// governed source is decided by its installation's roles, which the cast
	// is not asked for here. Any tie refuses, live or not, so the decision
	// reads nothing more. Decided here, so the check after the receipt guard's
	// hold makes it too.
	if holder.Cast() && action == personalSourceBoundaryAction && (src.PersonalOwnerUserID != actor || src.Governance != nil) {
		return nil, status.Errorf(codes.PermissionDenied,
			"this session acts as team %s and as nothing else; a cast calls only a source of its person's own: use the session the cast was opened from", holder.SubjectID)
	}
	allowed, err := s.holdsTx(ctx, org, holder, action, src.BoundaryNodeID)
	if err != nil {
		return nil, err
	}
	if !allowed && holder.Cast() {
		return nil, status.Error(codes.PermissionDenied, holder.lacks(action, src.BoundaryNodeID))
	}
	if !allowed {
		return nil, status.Error(codes.PermissionDenied, "source permission required")
	}
	return src, nil
}
