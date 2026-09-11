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
export { Dashboard } from "./dashboard.js";
export { formatAxisKey, formatAxisValue } from "./format.js";
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
	valuesExtent,
	axisPositions,
} from "./metric-geometry.js";

export { Sparkline } from "./sparkline.js";

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
