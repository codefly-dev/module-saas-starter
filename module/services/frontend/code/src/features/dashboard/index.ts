export type { AuditEventTypeInfo } from "@/features/audit";
export {
	type Bucket,
	type ChartKind,
	DASHBOARD_SPEC_VERSION,
	type DashboardDef,
	dashboard,
	type EventDef,
	event,
	type GroupBy,
	type LayoutDef,
	type MetricDef,
	metric,
	type ThemeDef,
} from "./model/schema";
export {
	assertDashboardSpec,
	DashboardSpecError,
	parseDashboardSpec,
} from "./model/validate";
export {
	type CommitResult,
	createDashboardAuthoring,
	type DashboardAuthoring,
	type DashboardAuthoringDeps,
	type EventTypeVocabulary,
	type FieldError,
	type MetricPreview,
	type PreviewResult,
} from "./service/authoring";
export { useDashboardAuthoring } from "./service/use-dashboard-authoring";
export {
	type DashboardDraft,
	useDashboardDraft,
} from "./service/use-dashboard-draft";
export { Dashboard } from "./ui/dashboard";
export { Layout } from "./ui/layout";
