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

func (s *Service) DeclareSourceOperations(ctx context.Context, actor, org, source string, declarations []operations.Declaration) ([]operations.Declaration, error) {
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
		if membership == nil || !IsOrgAdminRole(orgRoleToString(membership.GetRole())) {
			return status.Error(codes.PermissionDenied, "organization administrator required")
		}
		src, err := s.store.GetDatasourceSource(ctx, org, source)
		if err != nil {
			return status.Error(codes.Unavailable, "source unavailable")
		}
		if src == nil {
			return status.Error(codes.NotFound, "source not found")
		}
		if src.PersonalOwnerUserID != "" && src.PersonalOwnerUserID != actor {
			return status.Error(codes.PermissionDenied, "source is personal to another member")
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

func (s *Service) ListSourceOperations(ctx context.Context, actor, org, source string) ([]operations.Declaration, error) {
	store, ok := s.store.(SourceOperationStore)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "source operations unavailable")
	}
	var out []operations.Declaration
	err := s.store.WithOrgTx(ctx, org, func(ctx context.Context) error {
		src, err := s.sourceOperationAccess(ctx, actor, org, source, "read")
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
func (s *Service) sourceOperationAccess(ctx context.Context, actor, org, source, action string) (*DatasourceSource, error) {
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
	allowed, err := s.store.CanReadScopeNode(ctx, org, actor, gen.SubjectKind_SUBJECT_KIND_PRINCIPAL, "datasource", action, src.BoundaryNodeID)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "source authority unavailable")
	}
	if !allowed {
		return nil, status.Error(codes.PermissionDenied, "source permission required")
	}
	return src, nil
}
