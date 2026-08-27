package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// datasourceConnectHandler exposes the tenant-facing datasource management
// surface. Every RPC is org-gated at the handler layer (the business methods do
// not authz themselves): the mutating RPCs require org admin, the reads require
// org membership.
type datasourceConnectHandler struct{ svc *business.Service }

func (h *datasourceConnectHandler) AddGitHubSource(
	ctx context.Context,
	req *connect.Request[gen.AddGitHubSourceRequest],
) (*connect.Response[gen.AddGitHubSourceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	source, err := h.svc.AddGitHubSource(ctx, actorID, business.AddGitHubSourceInput{
		OrgID:            req.Msg.OrgId,
		Repo:             req.Msg.Repo,
		Paths:            req.Msg.Paths,
		Branch:           req.Msg.Branch,
		TargetCollection: req.Msg.TargetCollection,
		AccessToken:      req.Msg.AccessToken,
		WebhookSecret:    req.Msg.WebhookSecret,
	})
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.AddGitHubSourceResponse{Datasource: datasourceSourceToProto(source)}), nil
}

func (h *datasourceConnectHandler) ListSources(
	ctx context.Context,
	req *connect.Request[gen.ListSourcesRequest],
) (*connect.Response[gen.ListSourcesResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	sources, err := h.svc.ListDatasourceSources(ctx, req.Msg.OrgId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	out := make([]*gen.Datasource, 0, len(sources))
	for _, source := range sources {
		out = append(out, datasourceSourceToProto(source))
	}
	return connect.NewResponse(&gen.ListSourcesResponse{Datasources: out}), nil
}

func (h *datasourceConnectHandler) GetSource(
	ctx context.Context,
	req *connect.Request[gen.GetSourceRequest],
) (*connect.Response[gen.GetSourceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	source, err := h.svc.GetDatasourceSource(ctx, req.Msg.OrgId, req.Msg.Id)
	if err != nil {
		if errors.Is(err, business.ErrDatasourceSourceNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.GetSourceResponse{Datasource: datasourceSourceToProto(source)}), nil
}

func (h *datasourceConnectHandler) SyncSource(
	ctx context.Context,
	req *connect.Request[gen.SyncSourceRequest],
) (*connect.Response[gen.SyncSourceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	jobID, err := h.svc.SyncDatasourceSource(ctx, actorID, req.Msg.OrgId, req.Msg.Id)
	if err != nil {
		if errors.Is(err, business.ErrDatasourceSourceNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.SyncSourceResponse{JobId: jobID}), nil
}

func (h *datasourceConnectHandler) DeleteSource(
	ctx context.Context,
	req *connect.Request[gen.DeleteSourceRequest],
) (*connect.Response[gen.DeleteSourceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	if err := h.svc.DeleteDatasourceSource(ctx, actorID, req.Msg.OrgId, req.Msg.Id); err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.DeleteSourceResponse{}), nil
}

// datasourceSourceToProto projects the domain Source onto its non-secret proto
// representation. Credential and webhook-secret envelopes are deliberately not
// mapped — the wire type has no field for them.
func datasourceSourceToProto(source *business.DatasourceSource) *gen.Datasource {
	out := &gen.Datasource{
		Id:                source.ID,
		OrgId:             source.OrgID,
		Provider:          datasourceProviderToProto(source.Provider),
		TargetCollection:  source.TargetCollection,
		Status:            datasourceStatusToProto(source.Status),
		WebhookConfigured: source.WebhookConfigured(),
		CreatedAt:         timestamppb.New(source.CreatedAt),
		UpdatedAt:         timestamppb.New(source.UpdatedAt),
	}
	if source.Provider == business.DatasourceProviderGitHub {
		out.Github = &gen.GitHubDatasourceConfig{
			Repo:   source.Repo,
			Paths:  source.Paths,
			Branch: source.Branch,
		}
	}
	if source.LastSyncedAt != nil {
		out.LastSyncedAt = timestamppb.New(*source.LastSyncedAt)
	}
	return out
}

func datasourceProviderToProto(provider string) gen.DatasourceProvider {
	if provider == business.DatasourceProviderGitHub {
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB
	}
	return gen.DatasourceProvider_DATASOURCE_PROVIDER_UNSPECIFIED
}

func datasourceStatusToProto(status string) gen.DatasourceStatus {
	switch status {
	case business.DatasourceStatusActive:
		return gen.DatasourceStatus_DATASOURCE_STATUS_ACTIVE
	case business.DatasourceStatusPaused:
		return gen.DatasourceStatus_DATASOURCE_STATUS_PAUSED
	default:
		return gen.DatasourceStatus_DATASOURCE_STATUS_UNSPECIFIED
	}
}
