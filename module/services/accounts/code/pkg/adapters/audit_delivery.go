package adapters

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"github.com/codefly-dev/core/workcontext"
	"github.com/codefly-dev/sdk-go/receipts"
	"github.com/codefly-dev/sdk-go/receipts/connecttransport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

// Delivery reads across tenants. A login or API key cannot substitute for the
// installed module's scoped operation context, including on receipt replay.
func authenticateAuditDeliveryContext(ctx context.Context, headers http.Header, lookup bool) (context.Context, error) {
	authority := WorkContextSingleton()
	if !authority.configured() {
		return ctx, connect.NewError(connect.CodeUnavailable, errors.New("audit delivery authority unavailable"))
	}
	values := headers.Values(workContextHeader)
	if len(values) != 1 {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("one audit delivery Work Context required"))
	}
	verified, err := authority.verifyFor(ctx, values[0], business.AuditDeliveryAudience)
	if err != nil {
		return ctx, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid audit delivery Work Context"))
	}
	claims := verified.Context()
	action := "invoke"
	if lookup {
		action = "read"
	}
	if claims.GetTenantId() == "" || claims.GetReplayPolicy() != workcontext.ReplayIdempotent ||
		!workContextGrants(claims, business.AuditDeliveryResourceKind, action, "") {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("audit delivery scope required"))
	}
	principal := claims.GetOwnerPrincipalId()
	if actor := verified.Actor(); actor != nil {
		principal = actor.GetPrincipalId()
	}
	seal := claims.GetSeal()
	if seal == nil {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("audit delivery installation seal required"))
	}
	installation, revision, epoch := seal.GetInstallationId(), seal.GetInstallationRevision(), seal.GetPrincipalEpoch()
	sealed := &business.SealedModuleAuthority{InstallationID: &installation, InstallationRevision: &revision, ProducerEpoch: &epoch}
	if seal.ImageDigest != nil && seal.BuildIncarnation != nil {
		digest, incarnation := seal.GetImageDigest(), seal.GetBuildIncarnation()
		sealed.ImageDigest, sealed.BuildIncarnation = &digest, &incarnation
	}
	current, err := service.AuthorizeModuleCapability(ctx, business.ModuleCaller{
		PrincipalID: principal, BoundOrg: claims.GetTenantId(), Sealed: sealed,
	})
	if err != nil {
		return ctx, translateGRPCError(err)
	}
	if !current.Grant.CrossTenant {
		return ctx, connect.NewError(connect.CodePermissionDenied, errors.New("cross-tenant audit delivery grant required"))
	}
	ctx = stampRequestIdentity(ctx, auth.OrdinaryRequestIdentity(claims.GetOwnerPrincipalId(), claims.GetTenantId()), auth.Assurance{})
	return ctx, nil
}

func (h *auditConnectHandler) DeliverAuditEvents(ctx context.Context, req *connect.Request[gen.DeliverAuditEventsRequest]) (*connect.Response[gen.DeliverAuditEventsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	ctx, release, err := service.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	defer release()
	ctx, err = authenticateAuditDeliveryContext(ctx, req.Header(), false)
	if err != nil {
		return nil, err
	}
	effect := business.AuditDeliveryEffectID(req.Msg.From)
	if !sourceWireFits(req.Msg, 256) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("audit delivery input exceeds wire bound"))
	}
	normalized := connect.NewRequest(proto.CloneOf(req.Msg))
	normalized.Header().Set(receipts.EffectIDHeaderName, effect)
	store, guard := service.SourceOperationReceipts()
	if store == nil || guard == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("audit delivery receipts unavailable"))
	}
	ctx = business.WithSourceOperationRecheck(ctx, func(ctx context.Context) error {
		_, err := authenticateAuditDeliveryContext(ctx, req.Header(), false)
		return err
	})
	return connecttransport.WrapUnary(guard, business.DeliverAuditEventsMethod, func(ctx context.Context, r *connect.Request[gen.DeliverAuditEventsRequest]) (*connect.Response[gen.DeliverAuditEventsResponse], error) {
		var out *gen.DeliverAuditEventsResponse
		_, err := service.DeliverAuditEvents(ctx, r.Msg.From, r.Msg.To, func(ctx context.Context, result business.AuditDeliveryResult) error {
			out = &gen.DeliverAuditEventsResponse{EffectId: effect, Status: "committed", Delivered: int32(result.Delivered)}
			return service.RecordSourceOperationReceipt(ctx, out)
		})
		if err != nil {
			return nil, translateGRPCError(err)
		}
		return connect.NewResponse(out), nil
	})(ctx, normalized)
}

func (h *auditConnectHandler) LookupDeliverAuditEvents(ctx context.Context, req *connect.Request[gen.DeliverAuditEventsRequest]) (*connect.Response[gen.DeliverAuditEventsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, translateGRPCError(err)
	}
	ctx, release, err := service.BeginModuleAuthorityUse(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	defer release()
	ctx, err = authenticateAuditDeliveryContext(ctx, req.Header(), true)
	if err != nil {
		return nil, err
	}
	// The runtime POSTs the invocation's original body again for recovery.
	// Its transport effect id identifies a task operation; the host receipt is
	// keyed independently by the journal range's from position.
	if _, err := business.ParseJournalPosition(req.Msg.From); err != nil {
		return nil, translateGRPCError(err)
	}
	effect := business.AuditDeliveryEffectID(req.Msg.From)
	tenant, _, _ := auth.VerifiedDatabaseIdentity(ctx)
	store, _ := service.SourceOperationReceipts()
	if store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("audit delivery receipts unavailable"))
	}
	saved, found, err := store.Lookup(ctx, tenant, effect, business.DeliverAuditEventsMethod)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("audit delivery receipt unavailable"))
	}
	if !found {
		// A generic Runnable owner reports absence as NOT_FOUND. A 200
		// response would be read as proof of a committed effect.
		return nil, connect.NewError(connect.CodeNotFound, errors.New("audit delivery outcome unknown"))
	}
	digest, err := receipts.RequestDigest(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if !bytes.Equal(saved.RequestDigest, digest) {
		return nil, connect.NewError(connect.CodeAlreadyExists, receipts.ErrEffectIDReused)
	}
	out := &gen.DeliverAuditEventsResponse{}
	if proto.Unmarshal(saved.Response, out) != nil || !sourceWireFits(out, 1024) || out.EffectId != effect || out.Status != "committed" {
		return nil, connect.NewError(connect.CodeInternal, errors.New("audit delivery receipt invalid"))
	}
	return connect.NewResponse(out), nil
}

// Both transports use the same operation handler and receipt guard.
func (s *AuditServer) DeliverAuditEvents(ctx context.Context, req *gen.DeliverAuditEventsRequest) (*gen.DeliverAuditEventsResponse, error) {
	r := connect.NewRequest(req)
	copyAuditDeliveryMetadata(ctx, r.Header())
	out, err := (&auditConnectHandler{inner: s}).DeliverAuditEvents(ctx, r)
	if err != nil {
		return nil, auditDeliveryGRPCError(err)
	}
	return out.Msg, nil
}

func (s *AuditServer) LookupDeliverAuditEvents(ctx context.Context, req *gen.DeliverAuditEventsRequest) (*gen.DeliverAuditEventsResponse, error) {
	r := connect.NewRequest(req)
	copyAuditDeliveryMetadata(ctx, r.Header())
	out, err := (&auditConnectHandler{inner: s}).LookupDeliverAuditEvents(ctx, r)
	if err != nil {
		return nil, auditDeliveryGRPCError(err)
	}
	return out.Msg, nil
}

func copyAuditDeliveryMetadata(ctx context.Context, header http.Header) {
	md, _ := metadata.FromIncomingContext(ctx)
	for key, values := range md {
		for _, value := range values {
			header.Add(key, value)
		}
	}
}

func auditDeliveryGRPCError(err error) error {
	var transport *connect.Error
	if !errors.As(err, &transport) {
		return err
	}
	converted := status.New(codes.Code(transport.Code()), transport.Message())
	for _, detail := range transport.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		enriched, err := converted.WithDetails(protoadapt.MessageV1Of(value))
		if err == nil {
			converted = enriched
		}
	}
	return converted.Err()
}
