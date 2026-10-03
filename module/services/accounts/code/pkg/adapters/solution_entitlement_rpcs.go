package adapters

import (
	"context"

	"connectrpc.com/connect"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// SolutionEntitlementServer answers what one viewer may use inside one org
// (issue #949). Internal-tier: both the organization and the subject are request
// fields, so this is an authority oracle about an arbitrary principal and a bare
// tenant JWT must never be able to ask it. The caller that may is the
// auth-gateway, naming the tenant and viewer it projected from a verified
// identity.
type SolutionEntitlementServer struct {
	gen.UnsafeSolutionEntitlementServiceServer
}

func (s *SolutionEntitlementServer) ListSolutionEntitlements(
	ctx context.Context, req *gen.ListSolutionEntitlementsRequest,
) (*gen.ListSolutionEntitlementsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	resp, err := service.ListSolutionEntitlements(ctx, req)
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return resp, nil
}

var _ gen.SolutionEntitlementServiceServer = (*SolutionEntitlementServer)(nil)

// solutionEntitlementConnectHandler wraps the gRPC server so the same guard and
// handler body run for a Connect request. Catalog generation owns registration
// through the shared singleton binding.
type solutionEntitlementConnectHandler struct {
	inner *SolutionEntitlementServer
}

func (h *solutionEntitlementConnectHandler) ListSolutionEntitlements(
	ctx context.Context, req *connect.Request[gen.ListSolutionEntitlementsRequest],
) (*connect.Response[gen.ListSolutionEntitlementsResponse], error) {
	return unary(ctx, req, h.inner.ListSolutionEntitlements)
}
