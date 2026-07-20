package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// consentConnectHandler exposes per-user TOS / privacy acceptance
// state. Both methods require auth — anonymous traffic doesn't have
// a user to record consent against. The FE drops the ConsentBanner
// based on (acceptedVersion, currentVersion) inequality.
type consentConnectHandler struct{ svc *business.Service }

func (h *consentConnectHandler) GetStatus(
	ctx context.Context,
	req *connect.Request[gen.GetConsentStatusRequest],
) (*connect.Response[gen.ConsentStatus], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	st, err := h.svc.GetConsentStatus(ctx, userID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(consentToProto(st)), nil
}

func (h *consentConnectHandler) Accept(
	ctx context.Context,
	req *connect.Request[gen.AcceptConsentRequest],
) (*connect.Response[gen.ConsentStatus], error) {
	ctx = connectCtx(ctx, req.Header())
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.AcceptConsent(ctx, userID, req.Msg.Version); err != nil {
		return nil, translateGRPCError(err)
	}
	st, _ := h.svc.GetConsentStatus(ctx, userID)
	return connect.NewResponse(consentToProto(st)), nil
}

func consentToProto(st *business.UserConsentStatus) *gen.ConsentStatus {
	if st == nil {
		return &gen.ConsentStatus{CurrentVersion: business.CurrentTermsVersion}
	}
	out := &gen.ConsentStatus{
		AcceptedVersion: st.AcceptedVersion,
		CurrentVersion:  st.CurrentVersion,
	}
	if st.AcceptedAt != nil {
		out.AcceptedAt = timestamppb.New(*st.AcceptedAt)
	}
	return out
}
