package adapters

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/codefly-dev/core/wool"

	"api/pkg/business"
	"api/pkg/gen"
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
	w := wool.Get(ctx).In("UpdateOrgSettings")
	w.GRPC().Inject()
	actorID, found := w.UserAuthID()
	if !found {
		return nil, status.Error(codes.Unauthenticated, "user id not found")
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
	data, contentType, filename, err := service.ExportAuditLog(ctx, req.OrgId, req.Format, req.ActorId, req.Action)
	if err != nil {
		return nil, err
	}
	return &gen.ExportAuditLogResponse{
		Data:        data,
		ContentType: contentType,
		Filename:    filename,
	}, nil
}
