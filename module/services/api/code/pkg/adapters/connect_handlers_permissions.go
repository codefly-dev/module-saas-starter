package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/gen"
)

// =====================================================================
// PrincipalService Connect adapter
// =====================================================================
//
// Wraps the gRPC PrincipalServer so the existing handler bodies
// (auth gates, validation, manifest-ceiling for Decide) run
// unchanged when invoked from a Connect HTTP request. Pattern
// matches permConnectHandler / userConnectHandler.

type principalConnectHandler struct {
	inner *PrincipalServer
}

func (h *principalConnectHandler) GetPrincipal(ctx context.Context, req *connect.Request[gen.GetPrincipalRequest]) (*connect.Response[gen.Principal], error) {
	return unary(ctx, req, h.inner.GetPrincipal)
}

func (h *principalConnectHandler) GetAgentPrincipal(ctx context.Context, req *connect.Request[gen.GetAgentPrincipalRequest]) (*connect.Response[gen.Principal], error) {
	return unary(ctx, req, h.inner.GetAgentPrincipal)
}

func (h *principalConnectHandler) CreateAgentPrincipal(ctx context.Context, req *connect.Request[gen.CreateAgentPrincipalRequest]) (*connect.Response[gen.Principal], error) {
	return unary(ctx, req, h.inner.CreateAgentPrincipal)
}

func (h *principalConnectHandler) RevokePrincipal(ctx context.Context, req *connect.Request[gen.RevokePrincipalRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.RevokePrincipal)
}

func (h *principalConnectHandler) ListPrincipals(ctx context.Context, req *connect.Request[gen.ListPrincipalsRequest]) (*connect.Response[gen.ListPrincipalsResponse], error) {
	return unary(ctx, req, h.inner.ListPrincipals)
}

// =====================================================================
// DelegationService Connect adapter
// =====================================================================
//
// Three unary RPCs forward through the standard `unary` helper.
// WaitForDelegation is server-streaming and bridges the Connect
// stream to the same business-layer event channel the gRPC
// streaming handler uses — duplicating the loop is cleaner than
// trying to wrap connect.ServerStream into a grpc.ServerStream.
//
// Holds a reference to DelegationServer so the streaming path
// can pick minted tokens out of the in-memory cache (set by the
// DecideDelegation gRPC handler when an approval mints a token).

type delegationConnectHandler struct {
	inner *DelegationServer
}

func (h *delegationConnectHandler) RequestDelegation(ctx context.Context, req *connect.Request[gen.RequestDelegationRequest]) (*connect.Response[gen.RequestDelegationResponse], error) {
	return unary(ctx, req, h.inner.RequestDelegation)
}

func (h *delegationConnectHandler) DecideDelegation(ctx context.Context, req *connect.Request[gen.DecideDelegationRequest]) (*connect.Response[gen.DelegationGrant], error) {
	return unary(ctx, req, h.inner.DecideDelegation)
}

func (h *delegationConnectHandler) ListPendingDelegations(ctx context.Context, req *connect.Request[gen.ListPendingDelegationsRequest]) (*connect.Response[gen.ListPendingDelegationsResponse], error) {
	return unary(ctx, req, h.inner.ListPendingDelegations)
}

// WaitForDelegation streams terminal events for a grant over the
// Connect protocol. Re-implements the same business-loop the gRPC
// version uses so we don't have to bridge two stream types.
//
// The token-cache lookup on approve mirrors the gRPC handler —
// both share s.inner so DecideDelegation's mint visibility holds
// across protocols.
func (h *delegationConnectHandler) WaitForDelegation(ctx context.Context, req *connect.Request[gen.WaitForDelegationRequest], stream *connect.ServerStream[gen.DelegationEvent]) error {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return translateGRPCError(err)
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.GetOrgId()); err != nil {
		return translateGRPCError(err)
	}

	w := wool.Get(ctx).In("Connect.WaitForDelegation",
		wool.Field("grant_id", req.Msg.GetId()))

	events, err := service.WaitForDelegation(ctx, req.Msg.GetId(), req.Msg.GetOrgId())
	if err != nil {
		return translateGRPCError(status.Error(codes.Internal, err.Error()))
	}

	for ev := range events {
		out := &gen.DelegationEvent{
			Id:                 ev.GrantID,
			Status:             string(ev.Status),
			GrantorPrincipalId: ev.GrantorID,
			Reason:             ev.Reason,
		}
		if ev.DecidedAt != nil {
			out.DecidedAt = timestamppb.New(*ev.DecidedAt)
		}
		if ev.Status == business.GrantStatusApproved {
			if minted := h.inner.lookupMintedToken(ev.GrantID); minted != nil {
				out.ScopedAuthToken = minted.Token
				out.MintedTokenId = minted.TokenID
			} else {
				w.Warn("approved grant but no minted token in cache; agent will see empty token",
					wool.Field("grant_id", ev.GrantID))
			}
		}
		if err := stream.Send(out); err != nil {
			return err
		}
	}
	return nil
}
