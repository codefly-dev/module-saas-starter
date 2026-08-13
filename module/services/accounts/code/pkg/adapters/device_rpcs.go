package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Connect adapters for DeviceService (external-device ↔ org pairing) and
// EntitlementCheckService (the fail-closed service-to-service paywall probe).
//
// L1 gates per the descriptor policy:
//   - CreateClaimCode / RevokeDevice: org admin + devices:write scope ceiling.
//   - ListDevices: org member + devices:read scope ceiling.
//   - ClaimDevice: public — the claim code is the credential.
//   - CheckDeviceEntitlement: API-key caller with entitlements:check scope;
//     interactive sessions are rejected (service-to-service endpoint).

type deviceConnectHandler struct {
	svc *business.Service
}

func (h *deviceConnectHandler) CreateClaimCode(
	ctx context.Context,
	req *connect.Request[gen.CreateDeviceClaimCodeRequest],
) (*connect.Response[gen.CreateDeviceClaimCodeResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireScope(ctx, "devices:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.CreateDeviceClaimCode(ctx, actorID, req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}

func (h *deviceConnectHandler) ClaimDevice(
	ctx context.Context,
	req *connect.Request[gen.ClaimDeviceRequest],
) (*connect.Response[gen.ClaimDeviceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	response, err := h.svc.ClaimDevice(ctx, req.Msg)
	if err != nil {
		var quotaErr *business.EntitlementQuotaError
		switch {
		case errors.Is(err, business.ErrDeviceClaimCodeInvalid):
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		case errors.Is(err, business.ErrDeviceAlreadyClaimed):
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		case errors.As(err, &quotaErr):
			return nil, connect.NewError(connect.CodeResourceExhausted, quotaErr)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}

func (h *deviceConnectHandler) ListDevices(
	ctx context.Context,
	req *connect.Request[gen.ListDevicesRequest],
) (*connect.Response[gen.ListDevicesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireScope(ctx, "devices:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.ListDevices(ctx, req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}

func (h *deviceConnectHandler) RevokeDevice(
	ctx context.Context,
	req *connect.Request[gen.RevokeDeviceRequest],
) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireScope(ctx, "devices:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	response, err := h.svc.RevokeDevice(ctx, actorID, req.Msg)
	if err != nil {
		if errors.Is(err, business.ErrDeviceNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}

type entitlementCheckConnectHandler struct {
	svc *business.Service
}

// requireAPIKeyScope is the strict service-to-service variant of requireScope:
// the caller MUST have authenticated with an API key (scopes present on the
// context) and that key must carry the required scope. Interactive JWT
// sessions — which requireScope deliberately passes through — are rejected.
func requireAPIKeyScope(ctx context.Context, required string) error {
	if len(scopesFromContext(ctx)) == 0 {
		return connect.NewError(connect.CodePermissionDenied,
			errors.New("this endpoint requires an API key with scope "+required))
	}
	if err := requireScope(ctx, required); err != nil {
		return translateGRPCError(err)
	}
	return nil
}

func (h *entitlementCheckConnectHandler) CheckDeviceEntitlement(
	ctx context.Context,
	req *connect.Request[gen.CheckDeviceEntitlementRequest],
) (*connect.Response[gen.CheckDeviceEntitlementResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if _, err := callerID(ctx); err != nil {
		return nil, err
	}
	if err := requireAPIKeyScope(ctx, "entitlements:check"); err != nil {
		return nil, err
	}
	// Unknown keys and unentitled orgs are 200-decisions (active=false), never
	// errors — callers fail closed on any non-200.
	response, err := h.svc.CheckDeviceEntitlement(ctx, req.Msg)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(response), nil
}
