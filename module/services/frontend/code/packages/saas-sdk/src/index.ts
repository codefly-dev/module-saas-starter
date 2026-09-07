export { createSaasClient, type SaasClient } from "./client/client.js";
export { compileMetric, type EventTypeResolver } from "./datagraph/compile.js";
export {
	type DashboardData,
	defineDataGraph,
	type MetricId,
	type ResolvedWidget,
	runDashboard,
} from "./datagraph/dashboard.js";
export { runDataGraph, runMetric } from "./datagraph/run.js";
export type {
	AuditAggregateClient,
	AuditAggregateQuery,
	MetricContext,
	MetricPoint,
	MetricSeries,
} from "./datagraph/types.js";
// The generated, gateway-bound facade for the accounts API. `accounts.New(gw)`
// binds the generated Connect clients to a transport (the gateway seam), one
// accessor per exposed service, mirroring the Go SDK's `accounts.New(gw).audit()`:
//   import { accounts } from "@codefly-dev/saas-sdk";
//   await accounts.New(gw).datasource().addGitHubSource({ orgId, repo });
export { accounts } from "../generated/typescript/src/accounts_facade.js";
export { AuditService } from "./gen/saas/accounts/v1/audit_pb.js";
export {
	type Datasource,
	DatasourceProvider,
	DatasourceService,
	DatasourceStatus,
} from "./gen/saas/accounts/v1/datasource_pb.js";
export { WebhookService } from "./gen/saas/accounts/v1/webhooks_pb.js";
export type {
	Dashboard,
	DashboardLayout,
	DataGraph,
	DerivedMetric,
	EventDeclaration,
	Metric,
	MetricAggregation,
	MetricBucket,
	MetricFilter,
	MetricGroupBy,
	MetricOperation,
	MetricWidget,
	SourceMetric,
	WidgetVisualization,
} from "./schema.js";
