export { createSaasClient, type SaasClient } from "./client/client.js";
export { compileMetric, type EventTypeResolver } from "./datagraph/compile.js";
export {
	type DashboardData,
	defineDataGraph,
	type MetricId,
	type ResolvedWidget,
	runDashboard,
	type WidgetValueFormat,
} from "./datagraph/dashboard.js";
export {
	assertAuditScopeContract,
	runDataGraph,
	runMetric,
} from "./datagraph/run.js";
export type {
	AuditAggregateClient,
	AuditAggregateQuery,
	MetricContext,
	MetricPoint,
	MetricSeries,
} from "./datagraph/types.js";
// The generated gateway-bound facade for the accounts/connect endpoint.
// `accounts.New(gw)` binds the generated Connect clients to a transport (the
// gateway seam); each accessor returns the typed client for one public service,
// mirroring the Go SDK's `accounts.New(gw).datasource()`:
//   import { accounts } from "@codefly-dev/saas-sdk";
//   await accounts.New(gw).datasource().addGitHubSource({ orgId, repo });
export { accounts } from "../generated/typescript/src/accounts_facade.js";
export {
	AccessBasis,
	type AccessibleScope,
	AccessibleScopeService,
	type ListAccessibleScopesResponse,
	type ListMyAccessibleScopesRequest,
} from "../generated/typescript/src/gen/saas/accounts/v1/accessible_scopes_pb.js";
export { AuditService } from "../generated/typescript/src/gen/saas/accounts/v1/audit_pb.js";
export {
	ApiCredentialKind,
	ApiOAuth2Config_Grant,
	type Datasource,
	type DatasourceAccountLink,
	DatasourceDomainStatus,
	type DatasourceGroupBinding,
	DatasourceLiveDelivery,
	DatasourceProvider,
	DatasourceService,
	DatasourceStatus,
	type DatasourceVerifiedDomain,
	type GetSourceSyncResponse,
	SourceOperation_Effect,
	type SourceSyncChanges,
	type SourceSyncFailure,
	SourceSyncFailureReason,
	SourceSyncPhase,
	type SourceSyncProgress,
	SourceSyncTrigger,
} from "../generated/typescript/src/gen/saas/accounts/v1/datasource_pb.js";
export {
	DirectoryService,
	type ListOrgMembersRequest,
	type ListOrgMembersResponse,
	type ListTeamMembersRequest,
	type ListTeamMembersResponse,
	type ListTeamsRequest,
	type ListTeamsResponse,
} from "../generated/typescript/src/gen/saas/accounts/v1/directory_pb.js";
export {
	type OrgMembership,
	OrgRole,
	type Team,
	type TeamMembership,
	TeamRole,
} from "../generated/typescript/src/gen/saas/accounts/v1/common_pb.js";
export { WebhookService } from "../generated/typescript/src/gen/saas/accounts/v1/webhooks_pb.js";
// The host interfaces a signed-in person's page calls, as every host serves
// them (this host and the local authority alike): the installation interface —
// installations, role grants and the setup read of an offer — and the session
// the bearer belongs to. Named `Host…` beside this package's own services.
export {
	type ConsentApproval,
	type Installation as HostInstallation,
	InstallationService as HostInstallationService,
	Installation_Status as HostInstallationStatus,
	type ListOffersResponse,
	type ListRoleGrantsResponse,
	type Offer,
	type OfferBinding,
	type OfferCapability,
	type OfferRole,
	type OfferScope,
	OfferService as HostOfferService,
	type OfferTool,
	type ReviewedOffer,
	type RoleGrant,
} from "../generated/typescript/src/gen/host/installation/v1/installation_pb.js";
export {
	type Session as HostSession,
	SessionService as HostSessionService,
} from "../generated/typescript/src/gen/host/session/v1/session_pb.js";
export type {
	Dashboard,
	DashboardLayout,
	DashboardSection,
	DataGraph,
	DerivedMetric,
	EventDeclaration,
	EventFieldDeclaration,
	EventFieldKind,
	Metric,
	MetricAggregation,
	MetricBucket,
	MetricFilter,
	MetricGroupBy,
	MetricOperation,
	MetricValueFormat,
	MetricWidget,
	SourceMetric,
	WidgetVisualization,
} from "./schema.js";
