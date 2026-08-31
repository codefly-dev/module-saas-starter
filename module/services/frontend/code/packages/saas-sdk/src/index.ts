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
// Gateway-bound facades, one per public service. `svc.New(gw)` binds the
// generated Connect client to a transport (the gateway seam), mirroring the Go
// SDK's `svc.New(gw).method(...)`:
//   import { datasource } from "@codefly/saas-sdk";
//   await datasource.New(gw).addGitHubSource({ orgId, repo });
export * as audit from "./facade/audit.js";
export * as datasource from "./facade/datasource.js";
export * as webhooks from "./facade/webhooks.js";
export { AuditService } from "./gen/saas/accounts/v1/audit_pb.js";
export { DatasourceService } from "./gen/saas/accounts/v1/datasource_pb.js";
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
