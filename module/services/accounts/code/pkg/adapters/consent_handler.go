package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

type consentConnectHandler struct {
	svc *business.Service
}

func (h *consentConnectHandler) GetStatus(
	ctx context.Context,
	req *connect.Request[gen.GetConsentStatusRequest],
) (*connect.Response[gen.ConsentStatus], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	status, err := h.svc.GetConsentStatus(ctx, userID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(consentToProto(status)), nil
}

func (h *consentConnectHandler) AcceptTerms(
	ctx context.Context,
	req *connect.Request[gen.AcceptTermsRequest],
) (*connect.Response[gen.ConsentStatus], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.AcceptTerms(ctx, userID, req.Msg.Version, req.Msg.Context); err != nil {
		return nil, translateGRPCError(err)
	}
	status, err := h.svc.GetConsentStatus(ctx, userID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(consentToProto(status)), nil
}

func (h *consentConnectHandler) UpdatePreferences(
	ctx context.Context,
	req *connect.Request[gen.UpdateConsentPreferencesRequest],
) (*connect.Response[gen.ConsentStatus], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.UpdateConsentPreferences(
		ctx,
		userID,
		req.Msg.PolicyVersion,
		req.Msg.Analytics,
		req.Msg.Marketing,
		req.Msg.Region,
		req.Msg.Context,
	); err != nil {
		return nil, translateGRPCError(err)
	}
	status, err := h.svc.GetConsentStatus(ctx, userID)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(consentToProto(status)), nil
}

func consentToProto(status *business.UserConsentStatus) *gen.ConsentStatus {
	if status == nil {
		return &gen.ConsentStatus{
			CurrentTermsVersion: business.CurrentTermsVersion,
			PolicyVersion:       business.CurrentConsentPolicyVersion,
		}
	}
	out := &gen.ConsentStatus{
		TermsAcceptedVersion:     status.TermsAcceptedVersion,
		CurrentTermsVersion:      status.CurrentTermsVersion,
		PolicyVersion:            status.PolicyVersion,
		PreferencesRecorded:      status.PreferencesRecorded,
		PreferencesPolicyVersion: status.PreferencesPolicyVersion,
	}
	if status.TermsAcceptedAt != nil {
		out.TermsAcceptedAt = timestamppb.New(*status.TermsAcceptedAt)
	}
	for _, preference := range status.Preferences {
		purpose := gen.ConsentPurpose_CONSENT_PURPOSE_UNSPECIFIED
		switch preference.Purpose {
		case "necessary":
			purpose = gen.ConsentPurpose_CONSENT_PURPOSE_NECESSARY
		case "analytics":
			purpose = gen.ConsentPurpose_CONSENT_PURPOSE_ANALYTICS
		case "marketing":
			purpose = gen.ConsentPurpose_CONSENT_PURPOSE_MARKETING
		}
		item := &gen.PurposeConsent{
			Purpose:   purpose,
			Granted:   preference.Granted,
			UpdatedAt: timestamppb.New(preference.UpdatedAt),
		}
		if preference.WithdrawnAt != nil {
			item.WithdrawnAt = timestamppb.New(*preference.WithdrawnAt)
		}
		out.Purposes = append(out.Purposes, item)
	}
	return out
}
