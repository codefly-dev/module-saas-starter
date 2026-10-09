// The shared dashboard kit: a pure, data-in renderer plus its charts and the
// chart atoms they compose from. Exported from `@codefly-dev/ui/dashboard` (a
// client subpath) so the host app and solution remotes render dashboards from
// one shared package instance. Pair with `@codefly-dev/saas-sdk`'s `runDashboard`
// for data resolution and `fromDashboardData` to bridge its result into the
// view model.
//
// The vocabulary these components draw is published, not just implemented:
// `catalog.ts` is the A2UI catalog naming them and the data model they bind to,
// and `@codefly-dev/ui/dashboard-catalog.json` is the exact document a consumer
// freezes. A visualization added to `WidgetVisualization` without a catalog entry
// does not compile.

export { Axis, Gridline, Svg, type XAxis, type YAxis } from "./atoms.js";
export {
	AreaChart,
	BarList,
	type ChartAxes,
	LineChart,
	StatChart,
} from "./charts.js";
export { Dashboard } from "./dashboard.js";
export {
	DASHBOARD_CATALOG,
	DASHBOARD_CATALOG_ID,
	DASHBOARD_CATALOG_PROTOCOL_VERSION,
	DASHBOARD_COMPONENT,
	DASHBOARD_DATA_SCHEMA,
	DASHBOARD_DATA_SCHEMA_ID,
	DASHBOARD_GRID_COMPONENT,
	DASHBOARD_VISUALIZATIONS,
	DASHBOARD_WIDGET_COMPONENT_BY_VISUALIZATION,
	type DashboardCatalogComponent,
	dashboardCatalogComponents,
	type JsonSchemaNode,
	METRIC_AREA_CHART_COMPONENT,
	METRIC_BAR_CHART_COMPONENT,
	METRIC_LINE_CHART_COMPONENT,
	STAT_TILE_COMPONENT,
} from "./catalog.js";
export { formatAxisKey, formatAxisValue, parseTimeKey } from "./format.js";
export {
	linearScale,
	niceTicks,
	type Plot,
	scaleX,
	scaleY,
} from "./geometry.js";
export {
	type DashboardLayoutKind,
	type DashboardView,
	type DashboardWidgetView,
	fromDashboardData,
	type SeriesPoint,
	type WidgetSeries,
	type WidgetVisualization,
} from "./types.js";

export {
	AreaChart as MetricAreaChart,
	LineChart as MetricLineChart,
	BarChart as MetricBarChart,
	chartSeriesColor,
	type MetricChartProps,
} from "./metric-chart.js";
export {
	type ChartDatum,
	type ChartSeries,
	type ResolvedSeries,
	type Point,
	unionLabels,
	resolveSeries,
	stackSeries,
	stackedExtent,
	type StackedSeries,
	valuesExtent,
	axisPositions,
} from "./metric-geometry.js";

export { Sparkline } from "./sparkline.js";
export { SortableGrid, type SortableGridProps } from "./sortable-grid.js";

export {
	MetricProvenance,
	MetricStateBadge,
	assertSampleModeAllowed,
	type MetricState,
} from "./metric-state.js";

export {
	MetricCard,
	StatTile,
	KPIRow,
	formatMetricValue,
	type Metric,
	type MetricFormat,
} from "./metric-tiles.js";

export { TrendLineChart } from "./trend-line-chart.js";
