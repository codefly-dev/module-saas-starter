export type { AuditEventTypeInfo } from "@/features/audit";
export {
	type Bucket,
	type ChartKind,
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	type Dimension,
	dashboard,
	type EventDef,
	event,
	type GroupBy,
	type LayoutDef,
	type MetricDef,
	type MetricOp,
	type MetricRatio,
	type MetricValue,
	metric,
	type ThemeDef,
} from "./model/schema";
export {
	type AuditVocabulary,
	assertDashboardSpec,
	DashboardSpecError,
	type FieldError,
	parseDashboardSpec,
	validateDashboard,
	validateMetric,
} from "./model/validate";
export {
	type CommitResult,
	createDashboardAuthoring,
	type DashboardAuthoring,
	type DashboardAuthoringDeps,
	type EventTypeVocabulary,
	type MetricPreview,
	type PreconditionCode,
	type PreviewResult,
} from "./service/authoring";
export { useDashboardAuthoring } from "./service/use-dashboard-authoring";
export {
	type DashboardDraft,
	useDashboardDraft,
} from "./service/use-dashboard-draft";
export { Dashboard } from "./ui/dashboard";
export { DashboardEditor } from "./ui/dashboard-editor";
export { Layout } from "./ui/layout";
