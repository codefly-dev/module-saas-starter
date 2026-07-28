package adapters

import (
	"context"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type waitlistConnectHandler struct {
	svc *business.Service
}

func (h *waitlistConnectHandler) GetAcquisitionStatus(
	_ context.Context,
	_ *connect.Request[gen.GetAcquisitionStatusRequest],
) (*connect.Response[gen.AcquisitionStatus], error) {
	return connect.NewResponse(h.svc.AcquisitionStatus()), nil
}

func (h *waitlistConnectHandler) Join(
	ctx context.Context,
	req *connect.Request[gen.JoinWaitlistRequest],
) (*connect.Response[gen.JoinWaitlistResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	response, err := h.svc.JoinWaitlist(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *waitlistConnectHandler) Verify(
	ctx context.Context,
	req *connect.Request[gen.VerifyWaitlistRequest],
) (*connect.Response[gen.VerifyWaitlistResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	response, err := h.svc.VerifyWaitlist(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *waitlistConnectHandler) List(
	ctx context.Context,
	req *connect.Request[gen.ListWaitlistRequest],
) (*connect.Response[gen.ListWaitlistResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.ListWaitlist(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *waitlistConnectHandler) Review(
	ctx context.Context,
	req *connect.Request[gen.ReviewWaitlistRequest],
) (*connect.Response[gen.WaitlistEntry], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.ReviewWaitlist(ctx, actorID, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (h *waitlistConnectHandler) Invite(
	ctx context.Context,
	req *connect.Request[gen.InviteWaitlistRequest],
) (*connect.Response[gen.WaitlistEntry], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformAdmin(ctx, actorID); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.InviteWaitlist(ctx, actorID, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}
