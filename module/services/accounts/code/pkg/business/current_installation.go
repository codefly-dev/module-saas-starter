package business

import (
	"context"
	"strings"

	gen "accounts/pkg/gen/saas/accounts/v1"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModuleCurrentInstallation returns identity only. Like the member-facing
// installation read it grants no permission to use the installed solution.
// The adapter supplies tenant, owner and audience from a verified current parent.
func (s *Service) ModuleCurrentInstallation(ctx context.Context, caller ModuleCaller, tenant, owner, audience, id string) (*gen.ModuleCurrentInstallationResponse, error) {
	grant, err := s.moduleGrant(caller)
	if err != nil {
		return nil, err
	}
	if err = authorizeTenant(caller, grant, tenant); err != nil {
		return nil, err
	}
	if tenant == "" || owner == "" || grant.Prefix == "" || audience != grant.Prefix || (grant.Tenant != "" && grant.Tenant != caller.BoundOrg) {
		return nil, status.Error(codes.PermissionDenied, "installation caller denied")
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return nil, status.Error(codes.InvalidArgument, "invalid installation selector")
	}
	var found *gen.Installation
	err = s.store.As(Identity{UserID: owner, OrgID: tenant}).Within(ctx, func(ctx context.Context) error {
		// Keep membership and installation reads inside the same tenant scope. The
		// current Work Context revision and actor checks are independent prerequisites.
		member, e := s.store.GetOrgMembership(ctx, tenant, owner)
		if e != nil {
			return e
		}
		if member == nil || member.Role == gen.OrgRole_ORG_ROLE_UNSPECIFIED {
			return status.Error(codes.PermissionDenied, "not a member of this organization")
		}
		found, _, e = s.installationStore().GetInstallation(ctx, tenant, id)
		return e
	})
	if err != nil {
		return nil, err
	}
	if found == nil || found.Id != id || found.OrgId != tenant || found.Status != gen.InstallationStatus_INSTALLATION_STATUS_ACTIVE || found.RevokedAt != nil || strings.TrimSpace(found.TargetId) == "" {
		return nil, status.Error(codes.NotFound, "active installation unavailable")
	}
	// The identity, not the alias. This returned `found.SolutionIdentifier` — a
	// free-text route alias a later binding may take — so a caller comparing it
	// could not tell one binding's presence from its replacement's. The target is
	// the consented identity and the binding is what an authority decision keys
	// on; a caller that wants the alias for display asks the catalogue read,
	// which serves accepted applied state and joins on this target id.
	//
	// A closed target is refused rather than reported: an installation whose
	// presence was withdrawn is not a current installation.
	//
	// The read is control-plane, for the reason requireConsentedBinding states:
	// `solution_targets` is global with exact grants and the request-path role
	// holds no SELECT on it.
	var target *SolutionTarget
	if err = s.store.WithControlPlane(ctx, func(ctx context.Context) error {
		var e error
		target, e = s.store.GetSolutionTarget(ctx, found.TargetId)
		return e
	}); err != nil {
		return nil, err
	}
	if !target.Live() || target.BindingID == "" {
		return nil, status.Error(codes.NotFound, "active installation unavailable")
	}
	return &gen.ModuleCurrentInstallationResponse{
		InstallationId: found.Id,
		TenantId:       found.OrgId,
		TargetId:       found.TargetId,
		BindingId:      target.BindingID,
	}, nil
}
