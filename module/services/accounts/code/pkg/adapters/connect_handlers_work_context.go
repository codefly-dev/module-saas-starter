package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// workContextConnectHandler is intentionally handwritten beside the
// permissions plugin. Catalog generation owns registration; the plugin owns the
// finite adapter and behavior.
type workContextConnectHandler struct {
	inner *WorkContextAuthorityServer
}

func (h *workContextConnectHandler) CheckAuthorizationRevision(
	ctx context.Context,
	req *connect.Request[gen.CheckAuthorizationRevisionRequest],
) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.CheckAuthorizationRevision)
}

func (h *workContextConnectHandler) AuthorizeEvidenceRead(
	ctx context.Context,
	req *connect.Request[gen.AuthorizeEvidenceReadRequest],
) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.AuthorizeEvidenceRead)
}

func (h *workContextConnectHandler) ConsumeSingleUse(
	ctx context.Context,
	req *connect.Request[gen.ConsumeSingleUseWorkContextRequest],
) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.ConsumeSingleUse)
}

func (h *workContextConnectHandler) StartTask(
	ctx context.Context,
	req *connect.Request[gen.StartTaskWorkContextRequest],
) (*connect.Response[gen.IssuedWorkContext], error) {
	return unary(ctx, req, h.inner.StartTask)
}

func (h *workContextConnectHandler) StartRootSession(
	ctx context.Context,
	req *connect.Request[gen.StartRootSessionWorkContextRequest],
) (*connect.Response[gen.IssuedWorkContext], error) {
	return unary(ctx, req, h.inner.StartRootSession)
}

func (h *workContextConnectHandler) ExchangeAudience(
	ctx context.Context,
	req *connect.Request[gen.ExchangeWorkContextAudienceRequest],
) (*connect.Response[gen.IssuedWorkContext], error) {
	return unary(ctx, req, h.inner.ExchangeAudience)
}

func (h *workContextConnectHandler) StartChildSession(
	ctx context.Context,
	req *connect.Request[gen.StartChildSessionWorkContextRequest],
) (*connect.Response[gen.IssuedWorkContext], error) {
	return unary(ctx, req, h.inner.StartChildSession)
}
