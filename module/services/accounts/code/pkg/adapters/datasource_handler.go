package adapters

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"accounts/pkg/business"
	"accounts/pkg/datasource/connector"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"accounts/pkg/jobs"
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
		OrgID:           req.Msg.OrgId,
		Repo:            req.Msg.Repo,
		Paths:           req.Msg.Paths,
		FileExtensions:  req.Msg.FileExtensions,
		Branch:          req.Msg.Branch,
		BoundaryNodeID:  req.Msg.GetBoundaryNodeId(),
		CollectionLabel: req.Msg.GetCollectionLabel(),
		AccessToken:     req.Msg.AccessToken,
		WebhookSecret:   req.Msg.WebhookSecret,
	})
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.AddGitHubSourceResponse{Datasource: datasourceSourceToProto(source, h.svc.DatasourceConnectors())}), nil
}

func (h *datasourceConnectHandler) AddSource(
	ctx context.Context,
	req *connect.Request[gen.AddSourceRequest],
) (*connect.Response[gen.AddSourceResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	input := business.AddSourceInput{
		OrgID:              req.Msg.OrgId,
		Provider:           datasourceProviderFromProto(req.Msg.Provider),
		BoundaryNodeID:     req.Msg.GetBoundaryNodeId(),
		CollectionLabel:    req.Msg.GetCollectionLabel(),
		Credential:         req.Msg.Credential,
		WebhookSecret:      req.Msg.WebhookSecret,
		OAuth2ClientSecret: req.Msg.Oauth2ClientSecret,
	}
	if gh := req.Msg.GetGithub(); gh != nil {
		input.Repo = gh.Repo
		input.Paths = gh.Paths
		input.FileExtensions = gh.FileExtensions
		input.Branch = gh.Branch
	}
	if api := req.Msg.GetApi(); api != nil {
		input.API = &business.APIDatasourceConfig{
			BaseURL:              api.BaseUrl,
			ResourcePath:         api.ResourcePath,
			CredentialKind:       apiCredentialKindFromProto(api.CredentialKind),
			CredentialHeader:     api.CredentialHeader,
			CredentialQueryParam: api.CredentialQueryParam,
		}
		if oauth := api.GetOauth2(); oauth != nil {
			input.API.OAuth2 = &business.APIOAuth2Config{
				TokenURL: oauth.TokenUrl,
				ClientID: oauth.ClientId,
				Scopes:   oauth.Scopes,
			}
		}
	}
	if c := req.Msg.GetCrawler(); c != nil {
		input.Crawler = &business.CrawlerDatasourceConfig{
			SitemapURL: c.SitemapUrl,
			MaxPages:   int(c.MaxPages),
		}
	}
	if up := req.Msg.GetUpload(); up != nil {
		input.Upload = &business.UploadDatasourceConfig{
			Endpoint:    up.Endpoint,
			Region:      up.Region,
			Bucket:      up.Bucket,
			Prefix:      up.Prefix,
			AccessKeyID: up.AccessKeyId,
			MaxObjects:  int(up.MaxObjects),
		}
	}
	source, err := h.svc.AddSource(ctx, actorID, input)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.AddSourceResponse{Datasource: datasourceSourceToProto(source, h.svc.DatasourceConnectors())}), nil
}

func (h *datasourceConnectHandler) GetDatasourceCatalog(
	ctx context.Context,
	req *connect.Request[gen.GetDatasourceCatalogRequest],
) (*connect.Response[gen.GetDatasourceCatalogResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	if _, err := callerID(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(datasourceCatalog(h.svc.DatasourceCatalog())), nil
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
		out = append(out, datasourceSourceToProto(source, h.svc.DatasourceConnectors()))
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
	return connect.NewResponse(&gen.GetSourceResponse{Datasource: datasourceSourceToProto(source, h.svc.DatasourceConnectors())}), nil
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
	jobID, err := h.svc.SyncDatasourceSource(ctx, actorID, req.Msg.OrgId, req.Msg.Id, req.Msg.AccessToken)
	if err != nil {
		var failure *jobs.ProcessingError
		if errors.As(err, &failure) {
			code := connect.CodeFailedPrecondition
			if failure.Retryable {
				code = connect.CodeUnavailable
			}
			return nil, connect.NewError(code, errors.New(failure.Failure.Message))
		}
		if errors.Is(err, business.ErrDatasourceSourceNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.SyncSourceResponse{JobId: jobID}), nil
}

func (h *datasourceConnectHandler) GetSourceSync(
	ctx context.Context,
	req *connect.Request[gen.GetSourceSyncRequest],
) (*connect.Response[gen.GetSourceSyncResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	operation, err := h.svc.GetDatasourceSync(ctx, req.Msg.OrgId, req.Msg.SourceId, req.Msg.JobId)
	if err != nil {
		if errors.Is(err, business.ErrDatasourceSourceNotFound) || errors.Is(err, business.ErrDatasourceSyncNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, translateGRPCError(err)
	}
	response := &gen.GetSourceSyncResponse{JobId: operation.JobID, State: operation.State}
	for _, delivery := range operation.Deliveries {
		response.Deliveries = append(response.Deliveries, &gen.SourceSyncDelivery{
			JobId: delivery.JobID, State: delivery.State, Execution: delivery.Execution,
		})
	}
	response.Progress = sourceSyncProgressToProto(operation.Progress)
	return connect.NewResponse(response), nil
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
func (h *datasourceConnectHandler) BeginGitHubAppSetup(
	ctx context.Context,
	req *connect.Request[gen.BeginGitHubAppSetupRequest],
) (*connect.Response[gen.BeginGitHubAppSetupResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	handle, err := h.svc.BeginGitHubAppSetup(ctx, actorID, req.Msg.OrgId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.BeginGitHubAppSetupResponse{
		InstallUrl: handle.InstallURL,
		State:      handle.State,
		ExpiresAt:  timestamppb.New(handle.ExpiresAt),
	}), nil
}

func (h *datasourceConnectHandler) CompleteGitHubAppSetup(
	ctx context.Context,
	req *connect.Request[gen.CompleteGitHubAppSetupRequest],
) (*connect.Response[gen.CompleteGitHubAppSetupResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	installation, err := h.svc.CompleteGitHubAppSetup(ctx, actorID, req.Msg.OrgId, req.Msg.State, req.Msg.InstallationId, req.Msg.Code)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	repositories := make([]*gen.GitHubAppRepository, 0, len(installation.Repositories))
	for _, repository := range installation.Repositories {
		repositories = append(repositories, &gen.GitHubAppRepository{
			Repo:             repository.Repo,
			DefaultBranch:    repository.DefaultBranch,
			AlreadyConnected: repository.AlreadyConnected,
		})
	}
	return connect.NewResponse(&gen.CompleteGitHubAppSetupResponse{
		InstallationId: installation.InstallationID,
		Repositories:   repositories,
	}), nil
}

func (h *datasourceConnectHandler) MigrateGitHubSourceToApp(
	ctx context.Context,
	req *connect.Request[gen.MigrateGitHubSourceToAppRequest],
) (*connect.Response[gen.MigrateGitHubSourceToAppResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actorID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actorID, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	source, err := h.svc.MigrateGitHubSourceToApp(ctx, actorID, req.Msg.OrgId, req.Msg.Id)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	return connect.NewResponse(&gen.MigrateGitHubSourceToAppResponse{
		Datasource: datasourceSourceToProto(source, h.svc.DatasourceConnectors()),
	}), nil
}

func datasourceSourceToProto(source *business.DatasourceSource, registry *connector.Registry) *gen.Datasource {
	conformant, gap := business.DatasourceConformance(registry, source.Provider)
	out := &gen.Datasource{
		Conformant:         conformant,
		ConformanceGap:     gap,
		Id:                 source.ID,
		OrgId:              source.OrgID,
		Provider:           datasourceProviderToProto(source.Provider),
		BoundaryNodeId:     source.BoundaryNodeID,
		BoundaryLabel:      source.BoundaryLabel,
		Status:             datasourceStatusToProto(source.Status),
		StatusReason:       source.StatusReason,
		WebhookConfigured:  source.WebhookConfigured(),
		CreatedAt:          timestamppb.New(source.CreatedAt),
		UpdatedAt:          timestamppb.New(source.UpdatedAt),
		LastIngestedCommit: source.LastIngestedCommit,
	}
	if source.Provider == business.DatasourceProviderGitHub {
		out.Github = &gen.GitHubDatasourceConfig{
			Repo:           source.Repo,
			Paths:          source.Paths,
			FileExtensions: source.FileExtensions,
			Branch:         source.Branch,
		}
	}
	if source.API != nil {
		out.Api = &gen.ApiDatasourceConfig{
			BaseUrl:              source.API.BaseURL,
			ResourcePath:         source.API.ResourcePath,
			CredentialKind:       apiCredentialKindToProto(source.API.CredentialKind),
			CredentialHeader:     source.API.CredentialHeader,
			CredentialQueryParam: source.API.CredentialQueryParam,
		}
		if source.API.OAuth2 != nil {
			out.Api.Oauth2 = &gen.ApiOAuth2Config{
				TokenUrl: source.API.OAuth2.TokenURL,
				ClientId: source.API.OAuth2.ClientID,
				Scopes:   source.API.OAuth2.Scopes,
			}
		}
	}
	if source.Crawler != nil {
		out.Crawler = &gen.CrawlerDatasourceConfig{
			SitemapUrl: source.Crawler.SitemapURL,
			MaxPages:   uint32(source.Crawler.MaxPages),
		}
	}
	if source.Upload != nil {
		out.Upload = &gen.UploadDatasourceConfig{
			Endpoint:    source.Upload.Endpoint,
			Region:      source.Upload.Region,
			Bucket:      source.Upload.Bucket,
			Prefix:      source.Upload.Prefix,
			AccessKeyId: source.Upload.AccessKeyID,
			MaxObjects:  uint32(source.Upload.MaxObjects),
		}
	}
	if source.LastSyncedAt != nil {
		out.LastSyncedAt = timestamppb.New(*source.LastSyncedAt)
	}
	if source.LastIngestedAt != nil {
		out.LastIngestedAt = timestamppb.New(*source.LastIngestedAt)
	}
	return out
}

func datasourceProviderToProto(provider string) gen.DatasourceProvider {
	switch provider {
	case business.DatasourceProviderGitHub:
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB
	case business.DatasourceProviderAPI:
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_API
	case business.DatasourceProviderCrawler:
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_CRAWLER
	case business.DatasourceProviderUpload:
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_UPLOAD
	default:
		return gen.DatasourceProvider_DATASOURCE_PROVIDER_UNSPECIFIED
	}
}

func datasourceProviderFromProto(provider gen.DatasourceProvider) string {
	switch provider {
	case gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB:
		return business.DatasourceProviderGitHub
	case gen.DatasourceProvider_DATASOURCE_PROVIDER_API:
		return business.DatasourceProviderAPI
	case gen.DatasourceProvider_DATASOURCE_PROVIDER_CRAWLER:
		return business.DatasourceProviderCrawler
	case gen.DatasourceProvider_DATASOURCE_PROVIDER_UPLOAD:
		return business.DatasourceProviderUpload
	default:
		return ""
	}
}

func apiCredentialKindFromProto(kind gen.ApiCredentialKind) string {
	switch kind {
	case gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER:
		return business.APICredentialKindBearer
	case gen.ApiCredentialKind_API_CREDENTIAL_KIND_BASIC:
		return business.APICredentialKindBasic
	case gen.ApiCredentialKind_API_CREDENTIAL_KIND_HEADER:
		return business.APICredentialKindHeader
	case gen.ApiCredentialKind_API_CREDENTIAL_KIND_QUERY:
		return business.APICredentialKindQuery
	case gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2:
		return business.APICredentialKindOAuth2
	default:
		return ""
	}
}

func apiCredentialKindToProto(kind string) gen.ApiCredentialKind {
	switch kind {
	case business.APICredentialKindBearer:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_BEARER
	case business.APICredentialKindBasic:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_BASIC
	case business.APICredentialKindHeader:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_HEADER
	case business.APICredentialKindQuery:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_QUERY
	case business.APICredentialKindOAuth2:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_OAUTH2
	default:
		return gen.ApiCredentialKind_API_CREDENTIAL_KIND_UNSPECIFIED
	}
}

// datasourceCatalog projects the host's connector registry onto the catalog a
// client renders the "connect a source" surface from: every registered
// provider, whether it conforms to the connector envelope and why not, and
// whether it accepts a new source. A client offers only those that do.
func datasourceCatalog(entries []business.DatasourceCatalogEntry) *gen.GetDatasourceCatalogResponse {
	out := &gen.GetDatasourceCatalogResponse{}
	for _, e := range entries {
		d := e.Descriptor
		entry := &gen.DatasourceProviderDescriptor{
			Provider:          datasourceProviderEnum[d.Key],
			Connector:         d.Key,
			DisplayName:       d.DisplayName,
			Description:       d.Description,
			SupportsWebhook:   d.SupportsWebhook,
			Interface:         datasourceInterfaceEnum[d.Interface],
			ReadersModel:      datasourceReadersModelEnum[d.Readers],
			Conformant:        d.Conformant,
			ConformanceGap:    d.Gap,
			AcceptsNewSources: e.AcceptsNewSources,
		}
		for _, m := range d.CredentialModes {
			entry.CredentialModes = append(entry.CredentialModes, datasourceCredentialModeEnum[m])
		}
		if d.Conformant {
			entry.Budget = &gen.DatasourceConnectorBudget{
				MaxItemsPerCall: uint32(d.Budget.MaxItemsPerCall),
				MaxBytesPerCall: d.Budget.MaxBytesPerCall,
				MaxItemBytes:    d.Budget.MaxItemBytes,
			}
		}
		for _, f := range d.ConfigFields {
			entry.ConfigFields = append(entry.ConfigFields, &gen.DatasourceConfigField{
				Key: f.Key, DisplayName: f.DisplayName, Help: f.Help, Required: f.Required,
			})
		}
		out.Providers = append(out.Providers, entry)
	}
	return out
}

var datasourceInterfaceEnum = map[connector.Interface]gen.DatasourceConnectorInterface{
	connector.InterfaceFiles:    gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_FILES,
	connector.InterfacePages:    gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_PAGES,
	connector.InterfaceRecords:  gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_RECORDS,
	connector.InterfaceMessages: gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_MESSAGES,
	connector.InterfaceEvents:   gen.DatasourceConnectorInterface_DATASOURCE_CONNECTOR_INTERFACE_EVENTS,
}

var datasourceCredentialModeEnum = map[connector.CredentialMode]gen.DatasourceCredentialMode{
	connector.CredentialNone:         gen.DatasourceCredentialMode_DATASOURCE_CREDENTIAL_MODE_NONE,
	connector.CredentialOrgApp:       gen.DatasourceCredentialMode_DATASOURCE_CREDENTIAL_MODE_ORG_APP,
	connector.CredentialUserOAuth:    gen.DatasourceCredentialMode_DATASOURCE_CREDENTIAL_MODE_USER_OAUTH,
	connector.CredentialStaticSecret: gen.DatasourceCredentialMode_DATASOURCE_CREDENTIAL_MODE_STATIC_SECRET,
}

var datasourceReadersModelEnum = map[connector.ReadersModel]gen.DatasourceReadersModel{
	connector.ReadersSourceScoped: gen.DatasourceReadersModel_DATASOURCE_READERS_MODEL_SOURCE_SCOPED,
	connector.ReadersTranslated:   gen.DatasourceReadersModel_DATASOURCE_READERS_MODEL_TRANSLATED,
}

// datasourceProviderEnum maps a connector's registry key onto the wire's
// provider enum.
var datasourceProviderEnum = map[string]gen.DatasourceProvider{
	business.DatasourceProviderGitHub:  gen.DatasourceProvider_DATASOURCE_PROVIDER_GITHUB,
	business.DatasourceProviderAPI:     gen.DatasourceProvider_DATASOURCE_PROVIDER_API,
	business.DatasourceProviderCrawler: gen.DatasourceProvider_DATASOURCE_PROVIDER_CRAWLER,
	business.DatasourceProviderUpload:  gen.DatasourceProvider_DATASOURCE_PROVIDER_UPLOAD,
}

func datasourceStatusToProto(status string) gen.DatasourceStatus {
	switch status {
	case business.DatasourceStatusActive:
		return gen.DatasourceStatus_DATASOURCE_STATUS_ACTIVE
	case business.DatasourceStatusPaused:
		return gen.DatasourceStatus_DATASOURCE_STATUS_PAUSED
	case business.DatasourceStatusDegraded:
		return gen.DatasourceStatus_DATASOURCE_STATUS_DEGRADED
	default:
		return gen.DatasourceStatus_DATASOURCE_STATUS_UNSPECIFIED
	}
}
