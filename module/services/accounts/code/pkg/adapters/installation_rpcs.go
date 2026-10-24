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

func (s *InstallationServer) RetryInstallation(ctx context.Context, req *gen.RetryInstallationRequest) (*gen.Installation, error) {
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
	installation, err := service.RetryInstallation(ctx, actorID, req.GetOrgId(), req.GetInstallationId())
	if err != nil {
		return nil, mapInstallationError(err)
	}
	return installation, nil
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
	var scope *business.InstallationScope
	if req.Scope != nil {
		parsed, err := business.ParseInstallationScope(req.GetScope())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		scope = &parsed
	}
	installation, err := service.InstallSolution(ctx, actorID, &business.InstallSolutionParams{
		Scope:               scope,
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
// so a bare tenant JWT must never be able to ask this. The caller that may is
// the auth-gateway, naming either the tenant it projected from a verified
// identity (its viewer projection) or the tenant an edge-verified Work Context
// names (its declared module route's bearer-less arm), never one a request chose.
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
		out = append(out, availableSolutionProto(entry))
	}
	return &gen.ListAvailableSolutionsResponse{Solutions: out, NextPageToken: next}, nil
}

func availableSolutionProto(entry *business.AvailableSolutionTarget) *gen.AvailableSolution {
	return &gen.AvailableSolution{
		TargetId:          entry.TargetID,
		BindingId:         entry.BindingID,
		RouteAlias:        entry.RouteAlias,
		ReleasePublisher:  entry.ReleasePublisher,
		ReleaseName:       entry.ReleaseName,
		ReleaseVersion:    entry.ReleaseVersion,
		OpenedGeneration:  entry.OpenedGeneration,
		AppliedGeneration: entry.AppliedGeneration,
		Installed:         entry.Installed,
		Kind:              entry.Kind,
	}
}

// readinessProto carries the verdict and its conditions as the host worded them.
// Nothing is rephrased here: a condition's detail is the owning part of the
// host's own sentence, and a surface that shortened it would be inventing a
// reason.
func readinessProto(readiness business.InstallationReadiness) *gen.InstallationReadiness {
	unmet := make([]*gen.ReadinessCondition, 0, len(readiness.Unmet))
	for _, condition := range readiness.Unmet {
		unmet = append(unmet, &gen.ReadinessCondition{
			Kind:      condition.Kind,
			Subject:   condition.Subject,
			Detail:    condition.Detail,
			ClearedBy: condition.ClearedBy,
			Action:    condition.Action,
			SourceId:  condition.SourceID,
		})
	}
	return &gen.InstallationReadiness{Ready: readiness.Ready, Unmet: unmet}
}

// readinessConditionsProto carries a bare condition list — the one a grant
// returns for what it could not carry — through the same mapping the readiness
// verdict uses, so the two can never word the same condition differently.
func readinessConditionsProto(conditions []business.ReadinessCondition) []*gen.ReadinessCondition {
	return readinessProto(business.InstallationReadiness{Unmet: conditions}).GetUnmet()
}

// ListOrganizationInstallations serves an organization administrator's view of
// its own organization's installations. Authenticated and org-admin, exactly as
// ListAvailableSolutions is: the organization is a request field, and it is
// bound to the caller's own admin membership before anything is read, so no
// organization's administrator can list another's. requireOrgAdmin admits a
// platform super administrator for every organization, here as on every other
// organization-admin method; that inherited reach is pinned by
// TestInstallationExposureAdmitsPlatformSuperAdmin.
func (s *InstallationServer) ListOrganizationInstallations(
	ctx context.Context, req *gen.ListOrganizationInstallationsRequest,
) (*gen.ListOrganizationInstallationsResponse, error) {
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
	rows, next, err := service.ListOrganizationInstallations(
		ctx, req.GetOrgId(), req.GetPageToken(), int(req.GetPageSize()))
	if err != nil {
		return nil, mapInstallationExposureError(err)
	}
	out := make([]*gen.OrganizationInstallation, 0, len(rows))
	for _, row := range rows {
		item := &gen.OrganizationInstallation{
			Installation:   row.Summary.GetInstallation(),
			Health:         row.Summary.GetHealth(),
			RootScopePath:  row.Exposure.RootScopePath,
			GrantedTeams:   row.Exposure.GrantedTeams,
			InheritedTeams: row.Exposure.InheritedTeams,
		}
		if row.Target != nil {
			item.Target = availableSolutionProto(row.Target)
		}
		item.Readiness = readinessProto(row.Readiness)
		for _, reached := range row.ReachedModules {
			item.ReachedModules = append(item.ReachedModules, &gen.ReachedModule{
				Audience:       reached.Audience,
				InstallationId: reached.InstallationID,
				RouteAlias:     reached.RouteAlias,
			})
		}
		out = append(out, item)
	}
	return &gen.ListOrganizationInstallationsResponse{Installations: out, NextPageToken: next}, nil
}

// GrantInstallationToTeam exposes an installation to a team. The host resolves
// the installation's authority-root node; the caller names an installation and a
// team, never a scope path.
func (s *InstallationServer) GrantInstallationToTeam(
	ctx context.Context, req *gen.GrantInstallationToTeamRequest,
) (*gen.GrantInstallationToTeamResponse, error) {
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
	granted, err := service.GrantInstallationToTeam(ctx, actorID,
		req.GetOrgId(), req.GetInstallationId(), req.GetTeamId(), req.GetRoleId())
	if err != nil {
		return nil, mapInstallationExposureError(err)
	}
	// What the grant CARRIED, and where it carried nothing, travel back with the
	// grant. An administrator who has just exposed a solution learns here that a
	// module it reaches holds nothing — instead of a person learning it later as
	// a bare Forbidden on a page, which is the trail this replaces.
	carried := make([]*gen.CarriedModuleAccess, 0, len(granted.Carried))
	for _, entry := range granted.Carried {
		carried = append(carried, &gen.CarriedModuleAccess{
			Audience:       entry.Audience,
			InstallationId: entry.InstallationID,
			ScopePath:      entry.ScopePath,
			RoleId:         entry.RoleID,
			Scopes:         entry.Scopes,
		})
	}
	return &gen.GrantInstallationToTeamResponse{
		Grant:   granted.Grant,
		Carried: carried,
		Unmet:   readinessConditionsProto(granted.Unmet),
	}, nil
}

// RevokeInstallationFromTeam removes a team's grant at an installation. It is a
// narrowing under the policy log; a host that cannot witness it refuses, and the
// refusal carries the host's reason.
func (s *InstallationServer) RevokeInstallationFromTeam(
	ctx context.Context, req *gen.RevokeInstallationFromTeamRequest,
) (*emptypb.Empty, error) {
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
	if err := service.RevokeInstallationFromTeam(ctx, actorID,
		req.GetOrgId(), req.GetInstallationId(), req.GetTeamId(), req.GetRoleId()); err != nil {
		return nil, mapInstallationExposureError(err)
	}
	return &emptypb.Empty{}, nil
}

// mapInstallationExposureError maps the exposure RPCs' failures. Unlike
// mapInstallationError it answers NotFound for an installation the caller's
// organization does not have — another organization's installation id included,
// which RLS makes indistinguishable from one that never existed — and it names a
// narrowing the policy log could not witness as the refusal it is, in the host's
// words, rather than as an internal fault.
func mapInstallationExposureError(err error) error {
	if err == nil {
		return nil
	}
	var se *business.StoreError
	if errors.As(err, &se) {
		switch se.StoreErrorType {
		case business.ErrTypeNotFound:
			return status.Error(codes.NotFound, err.Error())
		case business.ErrTypeConflict:
			return status.Error(codes.FailedPrecondition, err.Error())
		case business.ErrTypePermission:
			return status.Error(codes.PermissionDenied, err.Error())
		case business.ErrTypeValidation:
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	if errors.Is(err, business.ErrPolicyLogUnreachable) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		return status.Error(st.Code(), err.Error())
	}
	return status.Error(codes.Internal, err.Error())
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
	if errors.Is(err, business.ErrPolicyLogUnreachable) {
		return status.Error(codes.Unavailable, err.Error())
	}
	if st, ok := status.FromError(err); ok {
		return st.Err()
	}
	return status.Error(codes.Internal, err.Error())
}

var _ gen.InstallationServiceServer = (*InstallationServer)(nil)
