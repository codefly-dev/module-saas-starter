package adapters

import (
	"context"
	"errors"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OrgSettings bridges between proto and business types.

func orgSettingsToProto(s *business.OrgSettings) *gen.OrgSettings {
	if s == nil {
		return nil
	}
	return &gen.OrgSettings{
		OrgId:        s.OrgID,
		LogoUrl:      s.LogoURL,
		PrimaryColor: s.PrimaryColor,
		CustomDomain: s.CustomDomain,
		FaviconUrl:   s.FaviconURL,
	}
}

func (s *OrgServer) GetOrgSettings(ctx context.Context, req *gen.GetOrgSettingsRequest) (*gen.OrgSettings, error) {
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
	settings, err := service.GetOrgSettings(ctx, req.OrgId)
	if err != nil {
		return nil, err
	}
	return orgSettingsToProto(settings), nil
}

func (s *OrgServer) UpdateOrgSettings(ctx context.Context, req *gen.UpdateOrgSettingsRequest) (*gen.OrgSettings, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Branding edits are an admin-scoped operation — plain members can't
	// redesign their org's customer-facing appearance.
	if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
		return nil, err
	}
	updated, err := service.UpdateOrgSettings(ctx, actorID, &business.OrgSettings{
		OrgID:        req.OrgId,
		LogoURL:      req.LogoUrl,
		PrimaryColor: req.PrimaryColor,
		CustomDomain: req.CustomDomain,
		FaviconURL:   req.FaviconUrl,
	})
	if err != nil {
		return nil, err
	}
	return orgSettingsToProto(updated), nil
}

func (s *AuditServer) ExportAuditLog(ctx context.Context, req *gen.ExportAuditLogRequest) (*gen.ExportAuditLogResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	// Export is a higher-privilege op than Query (it emits a file). An org's
	// administrators export their own org; a platform role exports no org it is
	// not an administrator of. No org_id is a platform-wide export.
	if req.OrgId == "" {
		if err := requirePlatformAdmin(ctx, actorID); err != nil {
			return nil, err
		}
	} else {
		if err := requireOrgAdmin(ctx, actorID, req.OrgId); err != nil {
			return nil, err
		}
	}
	data, contentType, filename, err := service.ExportAuditLog(ctx, req.OrgId, req.Format, req.ActorId, req.EventType, req.EventTypes)
	if err != nil {
		var storeErr *business.StoreError
		if errors.As(err, &storeErr) && storeErr.StoreErrorType == business.ErrTypeValidation {
			return nil, status.Error(codes.InvalidArgument, storeErr.Error())
		}
		return nil, err
	}
	return &gen.ExportAuditLogResponse{
		Data:        data,
		ContentType: contentType,
		Filename:    filename,
	}, nil
}
