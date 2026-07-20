package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// auditExportConnectHandler exposes per-org audit-export config CRUD.
// Org-admin gated at the handler layer (the underlying business
// methods don't authz themselves — they trust the adapter).
//
// Get masks the secret_access_key in the response. Save accepts
// secret_access_key="" to mean "preserve existing", so the FE never
// has to round-trip a redacted value.
type auditExportConnectHandler struct{ svc *business.Service }

func (h *auditExportConnectHandler) GetConfig(
	ctx context.Context,
	req *connect.Request[gen.GetAuditExportConfigRequest],
) (*connect.Response[gen.AuditExportConfig], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	cfg, err := h.svc.GetAuditExportConfig(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if cfg == nil {
		// 404 over "empty" so the FE can branch on existence.
		return nil, connect.NewError(connect.CodeNotFound, errAuditExportNotFound)
	}
	return connect.NewResponse(auditExportToProto(cfg)), nil
}

func (h *auditExportConnectHandler) SaveConfig(
	ctx context.Context,
	req *connect.Request[gen.SaveAuditExportConfigRequest],
) (*connect.Response[gen.AuditExportConfig], error) {
	ctx = connectCtx(ctx, req.Header())
	if req.Msg.Config == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errAuditExportNoBody)
	}
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.Config.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	in := auditExportFromProto(req.Msg.Config)
	if err := h.svc.SaveAuditExportConfig(ctx, in.OrgID, in); err != nil {
		return nil, translateGRPCError(err)
	}
	out, _ := h.svc.GetAuditExportConfig(ctx, in.OrgID)
	return connect.NewResponse(auditExportToProto(out)), nil
}

func (h *auditExportConnectHandler) DeleteConfig(
	ctx context.Context,
	req *connect.Request[gen.DeleteAuditExportConfigRequest],
) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteAuditExportConfig(ctx, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

// ─ converters ───────────────────────────────────────────────────

func auditExportToProto(c *business.AuditExportConfig) *gen.AuditExportConfig {
	if c == nil {
		return nil
	}
	out := &gen.AuditExportConfig{
		Id:              c.ID,
		OrgId:           c.OrgID,
		Bucket:          c.Bucket,
		Region:          c.Region,
		Endpoint:        c.Endpoint,
		Prefix:          c.Prefix,
		AccessKeyId:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey, // already masked by Service.GetAuditExportConfig
		CadenceMinutes:  int32(c.CadenceMinutes),
		Enabled:         c.Enabled,
		LastError:       c.LastError,
	}
	if c.LastExportedAt != nil {
		out.LastExportedAt = timestamppb.New(*c.LastExportedAt)
	}
	if c.LastErrorAt != nil {
		out.LastErrorAt = timestamppb.New(*c.LastErrorAt)
	}
	return out
}

func auditExportFromProto(p *gen.AuditExportConfig) *business.AuditExportConfig {
	return &business.AuditExportConfig{
		ID:              p.Id,
		OrgID:           p.OrgId,
		Bucket:          p.Bucket,
		Region:          p.Region,
		Endpoint:        p.Endpoint,
		Prefix:          p.Prefix,
		AccessKeyID:     p.AccessKeyId,
		SecretAccessKey: p.SecretAccessKey,
		CadenceMinutes:  int(p.CadenceMinutes),
		Enabled:         p.Enabled,
	}
}

type errString string

func (e errString) Error() string { return string(e) }

const (
	errAuditExportNotFound errString = "audit export config not configured"
	errAuditExportNoBody   errString = "config body required"
)
