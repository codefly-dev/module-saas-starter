export type { AuditEventTypeInfo } from "@/features/audit";
export {
	assertDashboardName,
	DashboardNameError,
	type DashboardRecord,
	type DashboardRecordPatch,
	type DashboardVisibility,
	DEFAULT_DASHBOARD_VISIBILITY,
	isDashboardVisibility,
} from "./model/record";
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
export {
	type CreateDashboardInput,
	createBrowserDashboardLibrary,
	createMemoryDashboardLibrary,
	DASHBOARD_LIBRARY_VERSION,
	type DashboardLibrary,
	type DashboardLibraryChange,
	dashboardRecordStore,
	driverDashboardStore,
} from "./service/dashboard-library";
export {
	createBrowserDashboardDraftStore,
	createMemoryDashboardDraftStore,
	type DashboardDraftChange,
	type DashboardDraftStore,
} from "./service/draft-store";
export { createServerDashboardLibrary } from "./service/server-dashboard-library";
export {
	scopedDashboardDraftKey,
	USER_DASHBOARD_DRAFT_KEY,
	USER_DASHBOARD_LIBRARY_KEY,
	useDashboardAuthoring,
} from "./service/use-dashboard-authoring";
export {
	type DashboardDraft,
	useDashboardDraft,
} from "./service/use-dashboard-draft";
export {
	type DashboardLibraryState,
	emptyDashboardSpec,
	useDashboardLibrary,
} from "./service/use-dashboard-library";
export { Dashboard } from "./ui/dashboard";
export { DashboardEditor } from "./ui/dashboard-editor";
export { Layout } from "./ui/layout";
export { MyDashboards } from "./ui/my-dashboards";
