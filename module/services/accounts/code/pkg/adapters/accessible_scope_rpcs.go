package adapters

import (
	"context"

	"connectrpc.com/connect"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// AccessibleScopeServer handles AccessibleScopeService RPCs — the caller-scoped
// view of the scope tree. It is a separate service from PermissionService so a
// published client can bind this read without also carrying the administrative
// authorization surface (role assignment, scope grants, record shares, and the
// decision oracles), which a per-file generated binding would otherwise drag in.
type AccessibleScopeServer struct {
	gen.UnsafeAccessibleScopeServiceServer
}

// ListMyAccessibleScopes is the authenticated, caller-scoped companion to
// PermissionService.ListAccessibleScopes: the subject is the bearer's own
// principal, so there is no subject_id in the request and it can never disclose
// another principal's boundaries. A normal org member reaches it through the
// gateway. It funnels into the same business method as the internal RPC, so the
// two resolve the identical grant + share union.
func (s *AccessibleScopeServer) ListMyAccessibleScopes(ctx context.Context, req *gen.ListMyAccessibleScopesRequest) (*gen.ListAccessibleScopesResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.ListAccessibleScopes(ctx, &gen.ListAccessibleScopesRequest{
		SubjectId:    actorID,
		SubjectKind:  gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		ResourceType: req.ResourceType,
		Action:       req.Action,
		OrgId:        req.OrgId,
		PageSize:     req.PageSize,
		PageToken:    req.PageToken,
	})
}

type accessibleScopeConnectHandler struct{ inner *AccessibleScopeServer }

func (h *accessibleScopeConnectHandler) ListMyAccessibleScopes(ctx context.Context, req *connect.Request[gen.ListMyAccessibleScopesRequest]) (*connect.Response[gen.ListAccessibleScopesResponse], error) {
	return unary(ctx, req, h.inner.ListMyAccessibleScopes)
}
