package adapters

import (
	"context"

	gen "accounts/pkg/gen/saas/accounts/v1"
)

// Generic org settings bridge between proto and business. Unlike the branding
// surface (rpcs_org_audit.go), the settings document is the generated protobuf
// itself end to end, so there is no parallel hand-written struct to convert.

func (s *OrgServer) GetOrganizationSettings(ctx context.Context, req *gen.GetOrganizationSettingsRequest) (*gen.OrganizationSettings, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	return service.GetOrgGenericSettings(ctx, req.OrgId)
}

func (s *OrgServer) UpdateOrganizationSettings(ctx context.Context, req *gen.UpdateOrganizationSettingsRequest) (*gen.OrganizationSettings, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Writing org-wide settings is an admin-scoped operation, mirroring the
	// branding surface: plain members read but do not change org settings.
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	var resetPaths []string
	if req.ClearMask != nil {
		resetPaths = req.ClearMask.Paths
	}
	return service.UpdateOrgGenericSettings(ctx, actorID, req.OrgId, req.Patch, resetPaths)
}
