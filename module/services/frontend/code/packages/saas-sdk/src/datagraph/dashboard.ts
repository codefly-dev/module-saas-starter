import type {
	DashboardLayout,
	DataGraph,
	WidgetVisualization,
} from "../schema.js";
import { runDataGraph } from "./run.js";
import type {
	AuditAggregateClient,
	MetricContext,
	MetricSeries,
} from "./types.js";

/**
 * Identity helper that preserves the literal metric ids of a data graph so
 * {@link runDashboard} can type its per-metric accessors. Declaring a graph
 * through this function is what turns the schema into typed accessors; a graph
 * parsed at runtime degrades to string-keyed access.
 */
export function defineDataGraph<const T extends DataGraph>(graph: T): T {
	return graph;
}

/** Metric ids declared on a data graph, as a literal union (or `string`). */
export type MetricId<T extends DataGraph> = T["metrics"][number]["id"];

/** A dashboard widget bound to its resolved metric series. */
export interface ResolvedWidget {
	id: string;
	visualization: WidgetVisualization;
	title?: string;
	metricId: string;
	series: MetricSeries;
}

/**
 * The `data={…}` a `<Dashboard>` renders: the dashboard's widgets bound to their
 * metric series, plus `byMetric` keyed by the graph's declared metric ids.
 */
export interface DashboardData<T extends DataGraph = DataGraph> {
	id: string;
	title?: string;
	layout: DashboardLayout;
	widgets: ResolvedWidget[];
	byMetric: Record<MetricId<T>, MetricSeries>;
}

/**
 * Resolve one dashboard's data graph into render-ready `data={…}`: every metric
 * is computed once, then each of the dashboard's widgets is bound to its
 * metric's series.
 */
export async function runDashboard<const T extends DataGraph>(
	client: AuditAggregateClient,
	graph: T,
	dashboardId: string,
	context: MetricContext,
): Promise<DashboardData<T>> {
	const dashboard = graph.dashboards.find((d) => d.id === dashboardId);
	if (!dashboard) {
		throw new Error(`unknown dashboard: ${dashboardId}`);
	}

	const byMetric = (await runDataGraph(client, graph, context)) as Record<
		MetricId<T>,
		MetricSeries
	>;

	const widgets = dashboard.widgets.map((widget): ResolvedWidget => {
		const series = byMetric[widget.metric as MetricId<T>];
		if (!series) {
			throw new Error(
				`widget '${widget.id}' is bound to unknown metric: ${widget.metric}`,
			);
		}
		return {
			id: widget.id,
			visualization: widget.visualization,
			title: widget.title,
			metricId: widget.metric,
			series,
		};
	});

	return {
		id: dashboard.id,
		title: dashboard.title,
		layout: dashboard.layout,
		widgets,
		byMetric,
	};
}
