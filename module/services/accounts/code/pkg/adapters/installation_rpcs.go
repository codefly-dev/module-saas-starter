package adapters

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// InstallationServer handles InstallationService RPCs — the install composition
// and its owner-of-record lifecycle. The headless mint that turns an installation
// into a Work Context is WorkContextService.StartInstallationTask, which owns the
// signer; this server holds no keys and delegates to the business Service.
type InstallationServer struct {
	gen.UnsafeInstallationServiceServer
}

func (s *InstallationServer) InstallSolution(ctx context.Context, req *gen.InstallSolutionRequest) (*gen.Installation, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	installation, err := service.InstallSolution(ctx, actorID, &business.InstallSolutionParams{
		OrgID:               req.GetOrgId(),
		AgentIdentifier:     req.GetAgentIdentifier(),
		TargetID:            req.GetTargetId(),
		DisplayName:         req.GetDisplayName(),
		RootScopeLabel:      req.GetRootScopeLabel(),
		RoleID:              req.GetRoleId(),
		OwnerPrincipalID:    req.GetOwnerPrincipalId(),
		CoOwnerPrincipalIDs: req.GetCoOwnerPrincipalIds(),
		AllowedAudiences:    req.GetAllowedAudiences(),
		AllowedScopes:       req.GetAllowedScopes(),
	})
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return installation, nil
}

func (s *InstallationServer) UninstallSolution(ctx context.Context, req *gen.UninstallSolutionRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	if err := service.UninstallSolution(ctx, actorID, req.GetOrgId(), req.GetInstallationId()); err != nil {
		return nil, mapInstallationError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *InstallationServer) TransferInstallationOwnership(ctx context.Context, req *gen.TransferInstallationOwnershipRequest) (*gen.Installation, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	installation, err := service.TransferInstallationOwnership(
		ctx, actorID, req.GetOrgId(), req.GetInstallationId(),
		req.GetNewOwnerPrincipalId(), req.GetCoOwnerPrincipalIds(),
	)
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return installation, nil
}

func (s *InstallationServer) GetInstallation(ctx context.Context, req *gen.GetInstallationRequest) (*gen.GetInstallationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	installation, health, err := service.GetInstallation(ctx, req.GetOrgId(), req.GetInstallationId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return &gen.GetInstallationResponse{Installation: installation, Health: health}, nil
}

// ListInstallations enumerates one organization's installations. Internal-tier,
// like the scope listing it is read beside: the organization is a request field,
// so a bare tenant JWT must never be able to ask this — the caller that may is
// the auth-gateway, naming the tenant it projected from a verified identity.
func (s *InstallationServer) ListInstallations(ctx context.Context, req *gen.ListInstallationsRequest) (*gen.ListInstallationsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if err := requireInternalCredential(ctx); err != nil {
		return nil, err
	}
	resp, err := service.ListInstallations(ctx, req)
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return resp, nil
}

// ListAvailableSolutions serves the catalogue. Authenticated and org-admin: it
// reports which presences this host has applied, which is operational state an
// organisation's administrator may see and a member may not — and it reports
// whether THIS organisation already installed each one, which is that
// organisation's own fact.
func (s *InstallationServer) ListAvailableSolutions(
	ctx context.Context, req *gen.ListAvailableSolutionsRequest,
) (*gen.ListAvailableSolutionsResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.GetOrgId()); err != nil {
		return nil, err
	}
	available, next, err := service.ListAvailableSolutions(
		ctx, req.GetOrgId(), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, mapInstallationError(err)
	}
	out := make([]*gen.AvailableSolution, 0, len(available))
	for _, entry := range available {
		out = append(out, &gen.AvailableSolution{
			TargetId:          entry.TargetID,
			BindingId:         entry.BindingID,
			RouteAlias:        entry.RouteAlias,
			ReleasePublisher:  entry.ReleasePublisher,
			ReleaseName:       entry.ReleaseName,
			ReleaseVersion:    entry.ReleaseVersion,
			OpenedGeneration:  entry.OpenedGeneration,
			AppliedGeneration: entry.AppliedGeneration,
			Installed:         entry.Installed,
		})
	}
	return &gen.ListAvailableSolutionsResponse{Solutions: out, NextPageToken: next}, nil
}

func mapInstallationError(err error) error {
	if err == nil {
		return nil
	}
	var se *business.StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case business.ErrTypeNotFound:
			return status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypeConflict:
			return status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return status.Error(codes.PermissionDenied, err.Error())
		case business.ErrTypeValidation:
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}

var _ gen.InstallationServiceServer = (*InstallationServer)(nil)
