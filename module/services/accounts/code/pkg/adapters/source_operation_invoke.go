package adapters

import (
	"context"
	"errors"
	"net/http"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/codefly-dev/sdk-go/receipts/connecttransport"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const sourceInputEnvelopeBytes = 65536
const sourceOutputEnvelopeBytes = 1048576

var sourceWireJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

func sourceWireFits(message proto.Message, bound int) bool {
	wire, err := sourceWireJSON.Marshal(message)
	return err == nil && len(wire) <= bound
}

// sourceOperationResponse is the only committed wire/receipt constructor.
func sourceOperationResponse(result *business.SourceOperationResult) (*gen.InvokeSourceOperationResponse, error) {
	output := string(result.Output)
	out := &gen.InvokeSourceOperationResponse{OutputJson: output, Receipt: &gen.SourceOperationReceipt{
		EffectId: result.EffectID, CommittedAt: result.CommittedAt.UTC().Format(time.RFC3339Nano), Status: "committed", ProviderStatus: uint32(result.ProviderStatus), OutputJson: output,
	}}
	if !sourceWireFits(out, sourceOutputEnvelopeBytes) {
		return nil, errors.New("source response exceeds wire bound")
	}
	return out, nil
}
func sourceUnknownResponse(effect string) *gen.InvokeSourceOperationResponse {
	return &gen.InvokeSourceOperationResponse{OutputJson: "{}", Receipt: &gen.SourceOperationReceipt{EffectId: effect, Status: "unknown", OutputJson: "{}"}}
}

func sourceEffectID(header http.Header, field string) (string, error) {
	ids := append(append([]string{}, header.Values(receipts.EffectIDHeaderName)...), header.Values(receipts.IdempotencyKeyHeaderName)...)
	if len(ids) > 1 || (len(ids) == 1 && field != "" && ids[0] != field) {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("ambiguous source effect id"))
	}
	if len(ids) == 1 {
		field = ids[0]
	}
	if field == "" || len(field) > 128 {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("bounded source effect id required"))
	}
	return field, nil
}

func (h *datasourceConnectHandler) InvokeSourceOperation(ctx context.Context, req *connect.Request[gen.InvokeSourceOperationRequest]) (response *connect.Response[gen.InvokeSourceOperationResponse], returnedErr error) {
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
	// Normalize the two effect carriers before the SDK computes the request hash.
	normalized := connect.NewRequest(proto.CloneOf(req.Msg))
	normalized.Msg.EffectId = effect
	normalized.Header().Set(receipts.EffectIDHeaderName, effect)
	if !sourceWireFits(normalized.Msg, sourceInputEnvelopeBytes) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("source input exceeds wire bound"))
	}
	var declaration operations.Declaration
	dispatched := false
	defer func() {
		if dispatched {
			return
		} // The engine audits every dispatched call exactly once.
		outcome := "refused"
		if returnedErr == nil {
			outcome = "committed"
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		h.svc.AuditSourceOperation(auditCtx, actor, req.Msg.OrgId, req.Msg.SourceId, declaration, effect, outcome)
	}()
	if err = requireOrgMember(ctx, actor, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err = requireSourceOperationScope(ctx, req.Msg.OrgId, req.Msg.SourceId, "invoke"); err != nil {
		return nil, err
	}
	_, declaration, err = h.svc.SourceOperationAuthority(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, req.Msg.Operation, effect, false)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	store, guard := h.svc.SourceOperationReceipts()
	if store == nil || guard == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	// Authenticated tenant is sealed before SDK replay; it never comes from the
	// untrusted org_id field alone.
	tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok || tenant != req.Msg.OrgId {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("source tenant mismatch"))
	}
	ctx = business.WithSourceOperationRecheck(ctx, func(ctx context.Context) error {
		if _, present := ctx.Value(sourceOperationContextKey{}).(*basev0.WorkContextV1); present {
			if _, err := authenticateSourceOperationContext(ctx, req.Header()); err != nil {
				return err
			}
		}
		_, _, err := h.svc.SourceOperationAuthority(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, req.Msg.Operation, effect, false)
		return translateGRPCError(err)
	})
	return connecttransport.WrapUnary(guard, business.SourceOperationMethod, func(ctx context.Context, r *connect.Request[gen.InvokeSourceOperationRequest]) (*connect.Response[gen.InvokeSourceOperationResponse], error) {
		dispatched = true
		var out *gen.InvokeSourceOperationResponse
		_, err := h.svc.InvokeSourceOperation(ctx, actor, r.Msg.OrgId, r.Msg.SourceId, r.Msg.Operation, []byte(r.Msg.InputJson), func(ctx context.Context, result *business.SourceOperationResult) error {
			var err error
			out, err = sourceOperationResponse(result)
			if err != nil {
				return err
			}
			return h.svc.RecordSourceOperationReceipt(ctx, out)
		})
		if err != nil {
			return nil, translateGRPCError(err)
		}
		return connect.NewResponse(out), nil
	})(ctx, normalized)
}

func (h *datasourceConnectHandler) LookupInvokeSourceOperation(ctx context.Context, req *connect.Request[gen.LookupInvokeSourceOperationRequest]) (*connect.Response[gen.InvokeSourceOperationResponse], error) {
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
	if err = requireOrgMember(ctx, actor, org); err != nil {
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
	if err = requireSourceOperationScope(ctx, org, attempt.SourceID, "read"); err != nil {
		return nil, err
	}
	if _, _, err = h.svc.SourceOperationAuthority(ctx, actor, org, attempt.SourceID, attempt.Operation, effect, true); err != nil {
		return nil, translateGRPCError(err)
	}
	store, _ := h.svc.SourceOperationReceipts()
	if store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	saved, found, err := store.Lookup(ctx, org, effect, business.SourceOperationMethod)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipt unavailable"))
	}
	if !found {
		return connect.NewResponse(sourceUnknownResponse(effect)), nil
	}
	out := &gen.InvokeSourceOperationResponse{}
	if proto.Unmarshal(saved.Response, out) != nil || !sourceWireFits(out, sourceOutputEnvelopeBytes) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("source receipt invalid"))
	}
	return connect.NewResponse(out), nil
}
