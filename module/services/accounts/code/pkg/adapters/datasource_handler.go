package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// datasourceConnectHandler exposes the org-owned datasource connection surface.
// Per-method authorization is enforced here (the sidecar only stamps identity);
// RLS scopes every store access to the caller's org.
type datasourceConnectHandler struct{ svc *business.Service }

func (h *datasourceConnectHandler) CreateSource(ctx context.Context, req *connect.Request[gen.CreateDatasourceSourceRequest]) (*connect.Response[gen.DatasourceSource], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "datasources:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	source := &business.DatasourceSource{
		OrgID:            req.Msg.OrgId,
		Connector:        connectorFromProto(req.Msg.Connector),
		DisplayName:      req.Msg.DisplayName,
		TargetCollection: req.Msg.TargetCollection,
		Config:           configFromProto(req.Msg.GetGithub()),
	}
	created, err := h.svc.CreateDatasourceSource(ctx, actorID, source)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(datasourceSourceToProto(created)), nil
}

func (h *datasourceConnectHandler) GetSource(ctx context.Context, req *connect.Request[gen.GetDatasourceSourceRequest]) (*connect.Response[gen.DatasourceSource], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "datasources:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgMember(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	source, err := h.svc.GetDatasourceSource(ctx, orgID, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(datasourceSourceToProto(source)), nil
}

func (h *datasourceConnectHandler) ListSources(ctx context.Context, req *connect.Request[gen.ListDatasourceSourcesRequest]) (*connect.Response[gen.ListDatasourceSourcesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "datasources:read"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	sources, err := h.svc.ListDatasourceSources(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.DatasourceSource, 0, len(sources))
	for _, source := range sources {
		out = append(out, datasourceSourceToProto(source))
	}
	return connect.NewResponse(&gen.ListDatasourceSourcesResponse{Sources: out}), nil
}

func (h *datasourceConnectHandler) DeleteSource(ctx context.Context, req *connect.Request[gen.DeleteDatasourceSourceRequest]) (*connect.Response[emptypb.Empty], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "datasources:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgAdmin(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteDatasourceSource(ctx, actorID, orgID, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *datasourceConnectHandler) SyncSource(ctx context.Context, req *connect.Request[gen.SyncDatasourceSourceRequest]) (*connect.Response[gen.DatasourceSource], error) {
	ctx = connectCtx(ctx, req.Header())
	if err := Validate(req.Msg); err != nil {
		return nil, err
	}
	if err := requireScope(ctx, "datasources:write"); err != nil {
		return nil, translateGRPCError(err)
	}
	actorID, err := requireAuth(ctx)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	orgID := callerOrg(ctx)
	if err := requireOrgAdmin(ctx, actorID, orgID); err != nil {
		return nil, translateGRPCError(err)
	}
	source, err := h.svc.RequestDatasourceSync(ctx, actorID, orgID, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(datasourceSourceToProto(source)), nil
}

func connectorFromProto(connector gen.DatasourceConnector) string {
	if connector == gen.DatasourceConnector_DATASOURCE_CONNECTOR_GITHUB {
		return business.DatasourceConnectorGitHub
	}
	return ""
}

func connectorToProto(connector string) gen.DatasourceConnector {
	if connector == business.DatasourceConnectorGitHub {
		return gen.DatasourceConnector_DATASOURCE_CONNECTOR_GITHUB
	}
	return gen.DatasourceConnector_DATASOURCE_CONNECTOR_UNSPECIFIED
}

func statusToProto(status string) gen.DatasourceSourceStatus {
	switch status {
	case business.DatasourceSourceStatusPending:
		return gen.DatasourceSourceStatus_DATASOURCE_SOURCE_STATUS_PENDING
	case business.DatasourceSourceStatusActive:
		return gen.DatasourceSourceStatus_DATASOURCE_SOURCE_STATUS_ACTIVE
	case business.DatasourceSourceStatusDisabled:
		return gen.DatasourceSourceStatus_DATASOURCE_SOURCE_STATUS_DISABLED
	case business.DatasourceSourceStatusError:
		return gen.DatasourceSourceStatus_DATASOURCE_SOURCE_STATUS_ERROR
	default:
		return gen.DatasourceSourceStatus_DATASOURCE_SOURCE_STATUS_UNSPECIFIED
	}
}

func configFromProto(github *gen.GitHubSourceConfig) business.DatasourceConfig {
	if github == nil {
		return business.DatasourceConfig{}
	}
	return business.DatasourceConfig{GitHub: &business.GitHubSourceConfig{
		Repository: github.Repository,
		Branch:     github.Branch,
		Paths:      github.Paths,
	}}
}

func datasourceSourceToProto(source *business.DatasourceSource) *gen.DatasourceSource {
	out := &gen.DatasourceSource{
		Id:               source.ID,
		OrgId:            source.OrgID,
		Connector:        connectorToProto(source.Connector),
		DisplayName:      source.DisplayName,
		TargetCollection: source.TargetCollection,
		Status:           statusToProto(source.Status),
		LastSyncError:    source.LastSyncError,
		CreatedAt:        timestamppb.New(source.CreatedAt),
		UpdatedAt:        timestamppb.New(source.UpdatedAt),
	}
	if github := source.Config.GitHub; github != nil {
		out.Config = &gen.DatasourceSource_Github{Github: &gen.GitHubSourceConfig{
			Repository: github.Repository,
			Branch:     github.Branch,
			Paths:      github.Paths,
		}}
	}
	if source.LastSyncRequestedAt != nil {
		out.LastSyncRequestedAt = timestamppb.New(*source.LastSyncRequestedAt)
	}
	if source.LastSyncedAt != nil {
		out.LastSyncedAt = timestamppb.New(*source.LastSyncedAt)
	}
	return out
}
