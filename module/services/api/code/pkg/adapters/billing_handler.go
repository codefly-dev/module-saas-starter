package adapters

import (
	"context"

	"connectrpc.com/connect"

	"api/pkg/business"
	"api/pkg/gen"
)

// billingConnectHandler — Connect-RPC surface for billing flows that
// the FE needs to invoke directly (without going through the
// auth-sidecar HTTP passthrough). Currently just OpenPortal; checkout
// stays on the REST path so paying customer flows match the documented
// auth-sidecar shape.
//
// Auth: org-admin gated. The portal session lets the holder change
// payment method / cancel subscription; we don't want a non-admin
// member to be able to do that.
type billingConnectHandler struct{ svc *business.Service }

func (h *billingConnectHandler) OpenPortal(
	ctx context.Context,
	req *connect.Request[gen.OpenBillingPortalRequest],
) (*connect.Response[gen.OpenBillingPortalResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	url, err := h.svc.OpenBillingPortal(ctx, business.OpenBillingPortalInput{
		UserID:    actorID,
		OrgID:     req.Msg.OrgId,
		ReturnURL: req.Msg.ReturnUrl,
	})
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.OpenBillingPortalResponse{Url: url}), nil
}
