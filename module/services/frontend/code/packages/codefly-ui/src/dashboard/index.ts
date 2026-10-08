// The shared dashboard kit: a pure, data-in renderer plus its charts and the
// chart atoms they compose from. Exported from `@codefly-dev/ui/dashboard` (a
// client subpath) so the host app and solution remotes render dashboards from
// one shared package instance. Pair with `@codefly-dev/saas-sdk`'s `runDashboard`
// for data resolution and `fromDashboardData` to bridge its result into the
// view model.

export { Axis, Gridline, Svg, type XAxis, type YAxis } from "./atoms.js";
export {
	AreaChart,
	BarList,
	type ChartAxes,
	LineChart,
	StatChart,
} from "./charts.js";
export {
	Dashboard,
	type DashboardSlots,
	type DashboardTile,
	DashboardWidget,
	type DashboardWidgetProps,
} from "./dashboard.js";
export { formatAxisKey, formatAxisValue, parseTimeKey } from "./format.js";
export {
	linearScale,
	niceTicks,
	type Plot,
	scaleX,
	scaleY,
} from "./geometry.js";
export {
	AreaChart as MetricAreaChart,
	BarChart as MetricBarChart,
	chartSeriesColor,
	LineChart as MetricLineChart,
	type MetricChartProps,
} from "./metric-chart.js";
export {
	axisPositions,
	type ChartDatum,
	type ChartSeries,
	type Point,
	type ResolvedSeries,
	resolveSeries,
	type StackedSeries,
	stackedExtent,
	stackSeries,
	unionLabels,
	valuesExtent,
} from "./metric-geometry.js";
export {
	assertSampleModeAllowed,
	MetricProvenance,
	type MetricState,
	MetricStateBadge,
} from "./metric-state.js";
export {
	formatMetricValue,
	KPIRow,
	type Metric,
	MetricCard,
	type MetricFormat,
	StatTile,
} from "./metric-tiles.js";
export { SortableGrid, type SortableGridProps } from "./sortable-grid.js";
export { Sparkline } from "./sparkline.js";
export { TrendLineChart } from "./trend-line-chart.js";
export {
	type DashboardLayoutKind,
	type DashboardSectionView,
	type DashboardView,
	type DashboardWidgetView,
	fromDashboardData,
	type SeriesPoint,
	type WidgetSeries,
	type WidgetVisualization,
} from "./types.js";
