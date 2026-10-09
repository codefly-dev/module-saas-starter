package adapters

import (
	"context"
	"errors"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/codefly-dev/sdk-go/receipts/connecttransport"
	"google.golang.org/protobuf/proto"
)

func (h *datasourceConnectHandler) requireReceiptRetention(ctx context.Context, actor, org, source, effect string, lookup bool) error {
	tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok || tenant != org {
		return connect.NewError(connect.CodePermissionDenied, errors.New("source tenant mismatch"))
	}
	if err := requireOrgAdmin(ctx, actor, org); err != nil {
		return translateGRPCError(err)
	}
	action := "invoke"
	if lookup {
		action = "read"
	}
	if err := requireSourceOperationScope(ctx, org, source, action); err != nil {
		return err
	}
	return translateGRPCError(h.svc.SourceReceiptRetentionAuthority(ctx, actor, org, source, effect, lookup))
}

func (h *datasourceConnectHandler) PruneSourceOperationReceipts(ctx context.Context, req *connect.Request[gen.PruneSourceOperationReceiptsRequest]) (*connect.Response[gen.PruneSourceOperationReceiptsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	actor, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	effect, err := sourceEffectID(req.Header(), req.Msg.EffectId)
	if err != nil {
		return nil, err
	}
	normalized := connect.NewRequest(proto.CloneOf(req.Msg))
	normalized.Msg.EffectId = effect
	normalized.Header().Set(receipts.EffectIDHeaderName, effect)
	if !sourceWireFits(normalized.Msg, 4096) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("source retention input exceeds wire bound"))
	}
	check := func(ctx context.Context) error {
		if _, present := ctx.Value(sourceOperationContextKey{}).(*basev0.WorkContextV1); present {
			if _, err := authenticateSourceOperationContext(ctx, req.Header()); err != nil {
				return err
			}
		}
		return h.requireReceiptRetention(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, effect, false)
	}
	// The transport already authenticated the initial Work Context. The SDK hold
	// gets a fresh authentication before it can replay a saved response.
	if err := h.requireReceiptRetention(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, effect, false); err != nil {
		return nil, err
	}
	store, guard := h.svc.SourceOperationReceipts()
	if store == nil || guard == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	ctx = business.WithSourceOperationRecheck(ctx, check)
	return connecttransport.WrapUnary(guard, business.SourceReceiptRetentionMethod, func(ctx context.Context, r *connect.Request[gen.PruneSourceOperationReceiptsRequest]) (*connect.Response[gen.PruneSourceOperationReceiptsResponse], error) {
		var out *gen.PruneSourceOperationReceiptsResponse
		err := h.svc.PruneSourceOperationReceipts(ctx, actor, r.Msg.OrgId, r.Msg.SourceId, func(ctx context.Context, removed int64) error {
			out = sourceRetentionResponse(effect, removed)
			return h.svc.RecordSourceOperationReceipt(ctx, out)
		})
		if err != nil {
			return nil, translateGRPCError(err)
		}
		return connect.NewResponse(out), nil
	})(ctx, normalized)
}

func (h *datasourceConnectHandler) LookupPruneSourceOperationReceipts(ctx context.Context, req *connect.Request[gen.LookupPruneSourceOperationReceiptsRequest]) (*connect.Response[gen.PruneSourceOperationReceiptsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	actor, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	org, _, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok || org == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("source tenant required"))
	}
	if err := requireOrgAdmin(ctx, actor, org); err != nil {
		return nil, translateGRPCError(err)
	}
	effect, err := sourceEffectID(req.Header(), req.Msg.EffectId)
	if err != nil {
		return nil, err
	}
	attempt, err := h.svc.SourceOperationAttemptForActor(ctx, actor, org, effect)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if attempt == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("source effect not found"))
	}
	if err := h.requireReceiptRetention(ctx, actor, org, attempt.SourceID, effect, true); err != nil {
		return nil, err
	}
	store, _ := h.svc.SourceOperationReceipts()
	if store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	saved, found, err := store.Lookup(ctx, org, effect, business.SourceReceiptRetentionMethod)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipt unavailable"))
	}
	if !found {
		return connect.NewResponse(sourceUnknownRetentionResponse(effect)), nil
	}
	out := &gen.PruneSourceOperationReceiptsResponse{}
	if proto.Unmarshal(saved.Response, out) != nil || !sourceWireFits(out, 4096) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("source receipt invalid"))
	}
	return connect.NewResponse(out), nil
}

func sourceRetentionResponse(effect string, removed int64) *gen.PruneSourceOperationReceiptsResponse {
	return &gen.PruneSourceOperationReceiptsResponse{EffectId: effect, Receipts: removed, Status: "committed"}
}
func sourceUnknownRetentionResponse(effect string) *gen.PruneSourceOperationReceiptsResponse {
	return &gen.PruneSourceOperationReceiptsResponse{EffectId: effect, Status: "unknown"}
}
