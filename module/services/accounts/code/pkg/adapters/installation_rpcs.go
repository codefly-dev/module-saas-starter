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
		SolutionIdentifier:  req.GetSolutionIdentifier(),
		DisplayName:         req.GetDisplayName(),
		RootScopePath:       req.GetRootScopePath(),
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
		}
	}
	return status.Error(codes.Internal, err.Error())
}

var _ gen.InstallationServiceServer = (*InstallationServer)(nil)
