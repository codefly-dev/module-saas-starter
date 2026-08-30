// The shared dashboard kit: a pure, data-in renderer plus its charts. Exported
// from `@codefly/ui/dashboard` (a client subpath) so the host app and solution
// remotes render dashboards from one shared package instance. Pair with
// `@codefly/saas-sdk`'s `runDashboard` for data resolution and `fromDashboardData`
// to bridge its result into the view model.

export { AreaChart, BarList, LineChart, StatChart } from "./charts.js";
export { Dashboard } from "./dashboard.js";
export {
	type DashboardLayoutKind,
	type DashboardView,
	type DashboardWidgetView,
	fromDashboardData,
	type SeriesPoint,
	type WidgetSeries,
	type WidgetVisualization,
} from "./types.js";
