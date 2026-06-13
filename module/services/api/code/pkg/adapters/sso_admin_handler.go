package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"api/pkg/business"
	"api/pkg/gen"
)

// ssoAdminConnectHandler exposes the per-org self-serve SSO surface.
// All three RPCs are org-admin-gated at the handler layer — the
// underlying business methods don't authz themselves.
type ssoAdminConnectHandler struct{ svc *business.Service }

func (h *ssoAdminConnectHandler) GetSSO(
	ctx context.Context,
	req *connect.Request[gen.GetOrgSSORequest],
) (*connect.Response[gen.OrgSSOConfig], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	cfg, err := h.svc.GetOrgSSO(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if cfg == nil {
		// Not configured. Return an empty proto rather than 404 so the
		// FE can render the "Set up SSO" CTA without an error toast.
		return connect.NewResponse(&gen.OrgSSOConfig{OrgId: req.Msg.OrgId}), nil
	}
	return connect.NewResponse(ssoToProto(cfg)), nil
}

func (h *ssoAdminConnectHandler) StartSetup(
	ctx context.Context,
	req *connect.Request[gen.StartSSOSetupRequest],
) (*connect.Response[gen.StartSSOSetupResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	link, err := h.svc.StartSSOSetup(ctx, actorID, req.Msg.OrgId, req.Msg.ReturnUrl)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.StartSSOSetupResponse{PortalLink: link}), nil
}

func (h *ssoAdminConnectHandler) Disable(
	ctx context.Context,
	req *connect.Request[gen.DisableSSORequest],
) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DisableSSO(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func ssoToProto(c *business.OrgSSOConfig) *gen.OrgSSOConfig {
	out := &gen.OrgSSOConfig{
		OrgId:          c.OrgID,
		Provider:       c.Provider,
		ConnectionId:   c.ConnectionID,
		OrganizationId: c.OrganizationID,
		Status:         c.Status,
	}
	if c.ConfiguredAt != nil {
		out.ConfiguredAt = timestamppb.New(*c.ConfiguredAt)
	}
	return out
}
