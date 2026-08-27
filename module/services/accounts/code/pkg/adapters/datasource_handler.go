package adapters

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// datasourceConnectHandler serves DatasourceService — the connection half of
// datasource ingestion. Access credentials are encrypted at rest and never
// returned by any read.
type datasourceConnectHandler struct{ svc *business.Service }

func (h *datasourceConnectHandler) AddGitHubSource(
	ctx context.Context,
	req *connect.Request[gen.AddGitHubSourceRequest],
) (*connect.Response[gen.GitHubDatasource], error) {
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
	ds, err := h.svc.AddGitHubSource(ctx, actorID, business.AddGitHubSourceInput{
		OrgID:      req.Msg.OrgId,
		Repo:       req.Msg.Repo,
		Paths:      req.Msg.Paths,
		Collection: req.Msg.Collection,
		Credential: req.Msg.Credential,
	})
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(datasourceToProto(ds)), nil
}

func (h *datasourceConnectHandler) ListSources(
	ctx context.Context,
	req *connect.Request[gen.ListSourcesRequest],
) (*connect.Response[gen.ListSourcesResponse], error) {
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
	sources, err := h.svc.ListDatasources(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.GitHubDatasource, 0, len(sources))
	for _, ds := range sources {
		out = append(out, datasourceToProto(ds))
	}
	return connect.NewResponse(&gen.ListSourcesResponse{Sources: out}), nil
}

func (h *datasourceConnectHandler) Sync(
	ctx context.Context,
	req *connect.Request[gen.SyncSourceRequest],
) (*connect.Response[gen.GitHubDatasource], error) {
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
	ds, err := h.svc.SyncDatasource(ctx, actorID, orgID, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(datasourceToProto(ds)), nil
}

func datasourceToProto(ds *business.Datasource) *gen.GitHubDatasource {
	if ds == nil {
		return nil
	}
	out := &gen.GitHubDatasource{
		Id:         ds.ID,
		OrgId:      ds.OrgID,
		Repo:       ds.Repo,
		Paths:      ds.Paths,
		Collection: ds.Collection,
		SyncStatus: datasourceSyncStatusToProto(ds.SyncStatus),
	}
	if !ds.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(ds.CreatedAt)
	}
	if !ds.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(ds.UpdatedAt)
	}
	if !ds.LastSyncRequestedAt.IsZero() {
		out.LastSyncRequestedAt = timestamppb.New(ds.LastSyncRequestedAt)
	}
	return out
}

func datasourceSyncStatusToProto(status string) gen.DatasourceSyncStatus {
	switch status {
	case business.DatasourceSyncStatusIdle:
		return gen.DatasourceSyncStatus_DATASOURCE_SYNC_STATUS_IDLE
	case business.DatasourceSyncStatusPending:
		return gen.DatasourceSyncStatus_DATASOURCE_SYNC_STATUS_PENDING
	default:
		return gen.DatasourceSyncStatus_DATASOURCE_SYNC_STATUS_UNSPECIFIED
	}
}
