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
export { AuditService } from "./gen/saas/accounts/v1/audit_pb.js";
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
