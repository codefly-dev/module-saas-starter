package adapters

import (
	"context"
	"errors"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// organizationLifecycleStatusError maps the organization lifecycle refusals to
// the status a caller can act on, with the sentinel's own text: the message is
// shown in the UI, and the internal call path is not the caller's to read.
func organizationLifecycleStatusError(err error) error {
	var storeErr *business.StoreError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, business.ErrOrgAdminContinuity):
		return status.Error(codes.FailedPrecondition, business.ErrOrgAdminContinuity.Error())
	case errors.Is(err, business.ErrOrgSoleMember):
		return status.Error(codes.FailedPrecondition, business.ErrOrgSoleMember.Error())
	case errors.Is(err, business.ErrOrgDeleteConfirmation):
		return status.Error(codes.InvalidArgument, business.ErrOrgDeleteConfirmation.Error())
	case errors.Is(err, business.ErrOrganizationCreationRefused):
		return status.Error(codes.PermissionDenied, business.ErrOrganizationCreationRefused.Error())
	case errors.Is(err, business.ErrOrganizationOwnerRefused):
		return status.Error(codes.PermissionDenied, business.ErrOrganizationOwnerRefused.Error())
	case errors.As(err, &storeErr) && storeErr.StoreErrorType == business.ErrTypeConflict:
		return status.Error(codes.AlreadyExists, "that slug is already taken")
	case errors.As(err, &storeErr) && storeErr.StoreErrorType == business.ErrTypeNotFound:
		return status.Error(codes.NotFound, "organization not found")
	case errors.As(err, &storeErr) && storeErr.StoreErrorType == business.ErrTypeValidation:
		return status.Error(codes.InvalidArgument, storeErr.Error())
	default:
		return quotaStatusError(err)
	}
}

func (s *OrgServer) UpdateOrganization(ctx context.Context, req *gen.UpdateOrganizationRequest) (*gen.Organization, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	org, err := service.UpdateOrganization(ctx, actorID, req)
	return org, organizationLifecycleStatusError(err)
}

func (s *OrgServer) LeaveOrganization(ctx context.Context, req *gen.LeaveOrganizationRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, userID, req.OrgId); err != nil {
		return nil, err
	}
	if err := service.LeaveOrganization(ctx, userID, req); err != nil {
		return nil, organizationLifecycleStatusError(err)
	}
	return &emptypb.Empty{}, nil
}

// DeleteOrganization is narrower than its descriptor's ORG_ADMIN tier: an
// administrator may manage an organization but only its owner — or a platform
// super administrator — may end it.
func (s *OrgServer) DeleteOrganization(ctx context.Context, req *gen.DeleteOrganizationRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// The declared floor first — administrator (a platform super administrator
	// passes) and a recent step-up when enrolled — then the narrower owner rule.
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	if err := requireOrgOwner(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	if err := requireMFA(ctx, actorID); err != nil {
		return nil, err
	}
	if err := service.DeleteOrganization(ctx, actorID, req); err != nil {
		return nil, organizationLifecycleStatusError(err)
	}
	return &emptypb.Empty{}, nil
}

// requireOrgOwner admits the organization's owner or a platform super
// administrator. The membership read is the caller's own pair, which is the only
// one lookupMembership answers.
func requireOrgOwner(ctx context.Context, actorID, orgID string) error {
	if role, err := platformRole(ctx, actorID); err == nil && role == "super_admin" {
		return nil
	}
	role, err := lookupMembership(ctx, orgID, actorID)
	if err != nil {
		return membershipLookupStatus("cannot verify membership", err)
	}
	if role == "" {
		return status.Error(codes.PermissionDenied, "not a member of this organization")
	}
	if role != "owner" {
		return status.Error(codes.PermissionDenied, "only the organization's owner may delete it")
	}
	return nil
}

func (s *InvitationServer) IssueInvitationLink(ctx context.Context, req *gen.IssueInvitationLinkRequest) (*gen.IssueInvitationLinkResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	var orgID string
	if err := service.Store().WithControlPlane(ctx, func(ctx context.Context) error {
		var resolveErr error
		orgID, resolveErr = service.Store().GetInvitationOrgID(ctx, req.Id)
		return resolveErr
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "cannot resolve invitation organization: %v", err)
	}
	if orgID == "" {
		return nil, status.Error(codes.NotFound, "invitation not found")
	}
	if err := requireOrgAdmin(ctx, actorID, orgID); err != nil {
		return nil, err
	}
	response, err := service.IssueInvitationLink(ctx, actorID, req, orgID)
	if errors.Is(err, business.ErrPublicOriginUnavailable) {
		return nil, status.Error(codes.FailedPrecondition, business.ErrPublicOriginUnavailable.Error())
	}
	return response, invitationStatusError(err)
}

func (s *PlatformAdminServer) ListAllOrganizations(ctx context.Context, req *gen.ListAllOrganizationsRequest) (*gen.ListAllOrganizationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, err
	}
	response, err := service.ListAllOrganizations(ctx, actorID, req)
	return response, organizationLifecycleStatusError(err)
}

func (s *PlatformAdminServer) GetOrganizationRoster(ctx context.Context, req *gen.GetOrganizationRosterRequest) (*gen.GetOrganizationRosterResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requirePlatformRole(ctx, actorID, "support"); err != nil {
		return nil, err
	}
	response, err := service.GetOrganizationRoster(ctx, actorID, req)
	return response, organizationLifecycleStatusError(err)
}

func (h *orgConnectHandler) UpdateOrganization(ctx context.Context, req *connect.Request[gen.UpdateOrganizationRequest]) (*connect.Response[gen.Organization], error) {
	return unary(ctx, req, h.inner.UpdateOrganization)
}
func (h *orgConnectHandler) LeaveOrganization(ctx context.Context, req *connect.Request[gen.LeaveOrganizationRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.LeaveOrganization)
}
func (h *orgConnectHandler) DeleteOrganization(ctx context.Context, req *connect.Request[gen.DeleteOrganizationRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, h.inner.DeleteOrganization)
}
func (h *invitationConnectHandler) IssueInvitationLink(ctx context.Context, req *connect.Request[gen.IssueInvitationLinkRequest]) (*connect.Response[gen.IssueInvitationLinkResponse], error) {
	return unary(ctx, req, h.inner.IssueInvitationLink)
}
func (h *platformAdminConnectHandler) ListAllOrganizations(ctx context.Context, req *connect.Request[gen.ListAllOrganizationsRequest]) (*connect.Response[gen.ListAllOrganizationsResponse], error) {
	return unary(ctx, req, h.inner.ListAllOrganizations)
}
func (h *platformAdminConnectHandler) GetOrganizationRoster(ctx context.Context, req *connect.Request[gen.GetOrganizationRosterRequest]) (*connect.Response[gen.GetOrganizationRosterResponse], error) {
	return unary(ctx, req, h.inner.GetOrganizationRoster)
}
