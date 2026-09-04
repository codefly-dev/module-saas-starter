package adapters

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
)

// moduleCapabilitiesConnectHandler serves the module-facing surface over the
// Connect protocol by delegating to the shared gRPC server, so both protocols
// enforce the same authority and see the same identity.
type moduleCapabilitiesConnectHandler struct {
	inner *ModuleCapabilitiesServer
}

func (h *moduleCapabilitiesConnectHandler) EnqueueJob(ctx context.Context, req *connect.Request[gen.ModuleEnqueueJobRequest]) (*connect.Response[gen.ModuleEnqueueJobResponse], error) {
	return unary(ctx, req, h.inner.EnqueueJob)
}

func (h *moduleCapabilitiesConnectHandler) ClaimJobs(ctx context.Context, req *connect.Request[gen.ModuleClaimJobsRequest]) (*connect.Response[gen.ModuleClaimJobsResponse], error) {
	return unary(ctx, req, h.inner.ClaimJobs)
}

func (h *moduleCapabilitiesConnectHandler) HeartbeatJob(ctx context.Context, req *connect.Request[gen.ModuleHeartbeatJobRequest]) (*connect.Response[gen.ModuleHeartbeatJobResponse], error) {
	return unary(ctx, req, h.inner.HeartbeatJob)
}

func (h *moduleCapabilitiesConnectHandler) AckJob(ctx context.Context, req *connect.Request[gen.ModuleAckJobRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.AckJob)
}

func (h *moduleCapabilitiesConnectHandler) NackJob(ctx context.Context, req *connect.Request[gen.ModuleNackJobRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.NackJob)
}

func (h *moduleCapabilitiesConnectHandler) NotifyUser(ctx context.Context, req *connect.Request[gen.ModuleNotifyUserRequest]) (*connect.Response[gen.ModuleNotifyUserResponse], error) {
	return unary(ctx, req, h.inner.NotifyUser)
}

func (h *moduleCapabilitiesConnectHandler) RequestApproval(ctx context.Context, req *connect.Request[gen.ModuleRequestApprovalRequest]) (*connect.Response[gen.ModuleRequestApprovalResponse], error) {
	return unary(ctx, req, h.inner.RequestApproval)
}

func (h *moduleCapabilitiesConnectHandler) GetApproval(ctx context.Context, req *connect.Request[gen.ModuleGetApprovalRequest]) (*connect.Response[gen.ModuleApproval], error) {
	return unary(ctx, req, h.inner.GetApproval)
}

func (h *moduleCapabilitiesConnectHandler) CancelApproval(ctx context.Context, req *connect.Request[gen.ModuleCancelApprovalRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.CancelApproval)
}

func (h *moduleCapabilitiesConnectHandler) EmitAuditEvent(ctx context.Context, req *connect.Request[gen.ModuleEmitAuditEventRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.EmitAuditEvent)
}
