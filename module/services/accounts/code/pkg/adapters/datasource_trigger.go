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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The administrator's surface for unattended work: the reference a release's
// triggers run under, the schedule this host declares, and the operation one
// occurrence admits.

func (h *datasourceConnectHandler) DelegateSourceTriggers(
	ctx context.Context,
	req *connect.Request[gen.DelegateSourceTriggersRequest],
) (*connect.Response[gen.DelegateSourceTriggersResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	references, err := h.svc.DelegateSourceTriggers(ctx, actorID, req.Msg.OrgId, req.Msg.SourceId)
	if err != nil {
		if errors.Is(err, business.ErrTriggerSourceNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.DelegateSourceTriggersResponse{
		Delegations: triggerReferencesToProto(references),
	}), nil
}

func (h *datasourceConnectHandler) GetSourceTriggerDeclaration(
	ctx context.Context,
	req *connect.Request[gen.GetSourceTriggerDeclarationRequest],
) (*connect.Response[gen.GetSourceTriggerDeclarationResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	declaration, references, children, err := h.svc.GetSourceTriggerDeclaration(ctx, req.Msg.OrgId, req.Msg.SourceId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.GetSourceTriggerDeclarationResponse{
		Declaration: triggerDeclarationToProto(declaration),
		Delegations: triggerReferencesToProto(references),
		Children:    triggerChildrenToProto(children),
	}), nil
}

// ReconcileSource is a host operation, so it runs the operation protocol rather
// than the plain request path: the effect identity is normalized from its two
// carriers before the SDK computes the request hash, authority is checked
// before the guard and again after the serialization hold, and the receipt
// commits in the business transaction.
func (h *datasourceConnectHandler) ReconcileSource(
	ctx context.Context,
	req *connect.Request[gen.ReconcileSourceRequest],
) (*connect.Response[gen.ReconcileSourceResponse], error) {
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
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("source reconcile input exceeds wire bound"))
	}
	if err := h.requireReconcile(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, effect, false); err != nil {
		return nil, err
	}
	store, guard := h.svc.SourceOperationReceipts()
	if store == nil || guard == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	check := func(ctx context.Context) error {
		if _, present := ctx.Value(sourceOperationContextKey{}).(*basev0.WorkContextV1); present {
			if _, err := authenticateSourceOperationContext(ctx, req.Header()); err != nil {
				return err
			}
		}
		return h.requireReconcile(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, effect, false)
	}
	ctx = business.WithSourceOperationRecheck(ctx, check)
	return connecttransport.WrapUnary(guard, business.ReconcileSourceMethod, func(ctx context.Context, r *connect.Request[gen.ReconcileSourceRequest]) (*connect.Response[gen.ReconcileSourceResponse], error) {
		var out *gen.ReconcileSourceResponse
		err := h.svc.ReconcileDatasourceSource(ctx, actor, r.Msg.OrgId, r.Msg.SourceId, func(ctx context.Context, jobID string) error {
			out = &gen.ReconcileSourceResponse{EffectId: effect, Status: "committed", JobId: jobID}
			return h.svc.RecordSourceOperationReceipt(ctx, out)
		})
		if err != nil {
			return nil, translateGRPCError(err)
		}
		return connect.NewResponse(out), nil
	})(ctx, normalized)
}

func (h *datasourceConnectHandler) LookupReconcileSource(
	ctx context.Context,
	req *connect.Request[gen.LookupReconcileSourceRequest],
) (*connect.Response[gen.ReconcileSourceResponse], error) {
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
	if err := requireOrgMember(ctx, actor, org); err != nil {
		return nil, translateGRPCError(err)
	}
	effect, err := sourceEffectID(req.Header(), req.Msg.EffectId)
	if err != nil {
		return nil, err
	}
	// The effect names the source, and the attempt row is the only thing that
	// knows which. A recovery that took the source from its own request could
	// read one effect's receipt while naming another source.
	attempt, err := h.svc.SourceOperationAttemptForActor(ctx, actor, org, effect)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if attempt == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("source effect not found"))
	}
	if err := h.requireReconcile(ctx, actor, org, attempt.SourceID, effect, true); err != nil {
		return nil, err
	}
	store, _ := h.svc.SourceOperationReceipts()
	if store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipts unavailable"))
	}
	saved, found, err := store.Lookup(ctx, org, effect, business.ReconcileSourceMethod)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("source receipt unavailable"))
	}
	if !found {
		// Absence is absence: a caller recovering an outcome reads "unknown"
		// and treats it as inconclusive, never as "it did not happen".
		return connect.NewResponse(&gen.ReconcileSourceResponse{EffectId: effect, Status: "unknown"}), nil
	}
	out := &gen.ReconcileSourceResponse{}
	if proto.Unmarshal(saved.Response, out) != nil || !sourceWireFits(out, 4096) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("source receipt invalid"))
	}
	return connect.NewResponse(out), nil
}

func (h *datasourceConnectHandler) requireReconcile(ctx context.Context, actor, org, source, effect string, lookup bool) error {
	tenant, _, ok := auth.VerifiedDatabaseIdentity(ctx)
	if !ok || tenant != org {
		return connect.NewError(connect.CodePermissionDenied, errors.New("source tenant mismatch"))
	}
	if err := requireOrgMember(ctx, actor, org); err != nil {
		return translateGRPCError(err)
	}
	return translateGRPCError(h.svc.ReconcileSourceAuthority(ctx, actor, org, source, effect, lookup))
}

func triggerDeclarationToProto(declaration business.SourceTriggerDeclaration) *gen.SourceTriggerDeclaration {
	out := &gen.SourceTriggerDeclaration{
		Release:            declaration.Release,
		Operation:          declaration.Operation,
		Digest:             declaration.Digest,
		UndeclarableReason: declaration.UndeclarableReason,
	}
	if declaration.LastOccurrenceAt != nil {
		out.LastOccurrenceAt = timestamppb.New(*declaration.LastOccurrenceAt)
	}
	if declaration.NextOccurrenceAt != nil {
		out.NextOccurrenceAt = timestamppb.New(*declaration.NextOccurrenceAt)
	}
	if !declaration.Declarable() {
		return out
	}
	// The projection is name-for-name with the declaration, which is the point:
	// what a reader sees is what a composition pastes into an installation, so
	// there is nothing to translate and nothing to get wrong between them.
	out.Schedule = &gen.DeclaredTriggerSchedule{
		Cron:      declaration.Schedule.Cron,
		Timezone:  declaration.Schedule.Timezone,
		MissedRun: declaration.Schedule.MissedRun,
		Overlap:   declaration.Schedule.Overlap,
		Deadline:  int64(declaration.Schedule.Deadline),
		InputJson: string(declaration.Schedule.Input),
	}
	out.Identity = &gen.TriggerIdentity{
		Source:  declaration.Identity.Source,
		Binding: declaration.Identity.Binding,
	}
	return out
}

func triggerReferencesToProto(references []business.TriggerDelegationReference) []*gen.TriggerDelegationReference {
	out := make([]*gen.TriggerDelegationReference, 0, len(references))
	for _, reference := range references {
		projected := &gen.TriggerDelegationReference{
			Reference:        reference.Reference,
			DelegationId:     reference.DelegationID,
			Module:           reference.Module,
			Binding:          reference.Binding,
			Audience:         reference.Audience,
			Tenant:           reference.Tenant,
			OwnerPrincipalId: reference.OwnerPrincipalID,
			Scopes:           reference.Scopes,
			Active:           reference.Active,
			CreatedAt:        timestamppb.New(reference.CreatedAt),
		}
		if reference.RevokedAt != nil {
			projected.RevokedAt = timestamppb.New(*reference.RevokedAt)
			projected.Revocation = sourceDelegationRevocations[reference.RevokedReason]
		}
		out = append(out, projected)
	}
	return out
}

func triggerChildrenToProto(children []business.TriggerChildContext) []*gen.TriggerChildContext {
	out := make([]*gen.TriggerChildContext, 0, len(children))
	for _, child := range children {
		out = append(out, &gen.TriggerChildContext{
			Record:       child.Record,
			DelegationId: child.DelegationID,
			Module:       child.Module,
			Binding:      child.Binding,
			Audience:     child.Audience,
			Scopes:       child.Scopes,
			Lookup:       child.Lookup,
			MintedAt:     timestamppb.New(child.MintedAt),
		})
	}
	return out
}
