package adapters

import (
	"context"

	"connectrpc.com/connect"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// userSettingsConnectHandler is intentionally thin: the generated protobuf is
// the canonical settings model in transport, business logic, SDK access, and
// persistence encoding. There is no parallel hand-written settings struct.
type userSettingsConnectHandler struct{ svc *business.Service }

func (h *userSettingsConnectHandler) Get(
	ctx context.Context,
	req *connect.Request[gen.GetUserSettingsRequest],
) (*connect.Response[gen.UserSettings], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	settings, err := h.svc.GetUserSettings(ctx, actorID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(settings), nil
}

func (h *userSettingsConnectHandler) Update(
	ctx context.Context,
	req *connect.Request[gen.UpdateUserSettingsRequest],
) (*connect.Response[gen.UserSettings], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	var resetPaths []string
	if req.Msg.ClearMask != nil {
		resetPaths = req.Msg.ClearMask.Paths
	}
	settings, err := h.svc.UpdateUserSettings(ctx, actorID, req.Msg.Patch, resetPaths)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(settings), nil
}
