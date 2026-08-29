"use client";

import type { DataGraph, MetricWidget } from "@codefly/saas-plugin-manifest";
import {
	createSaasClient,
	type ResolvedWidget,
	runDashboard,
} from "@codefly/saas-sdk";
import { useQuery } from "@tanstack/react-query";
import { BarList } from "@/features/dashboard/ui/charts/bar-list";
import { LineChart } from "@/features/dashboard/ui/charts/line-chart";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import {
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Grid,
	Skeleton,
	Stack,
} from "@/shared/ui";

// One audit-backed SDK client for every solution dashboard, built on the host's
// shared Connect transport so a solution's declared graph resolves through the
// same auth'd, org-scoped gateway path the rest of the app uses. The host owns
// the client, the bearer token, and the org scope; the solution supplies only
// the DataGraph — it can never widen a query past the viewer's own org.
const sdk = createSaasClient(apiTransport);

// Dashboard id of the throwaway single-widget graph each card resolves. The card
// resolves its own widget in isolation, so this id is never surfaced.
const SINGLE_WIDGET_DASHBOARD = "widget";

// A graph carrying every event and metric but exactly one widget, so runDashboard
// resolves only that widget's metric (and its derived inputs). Resolving each
// widget through its own call is what isolates failures: a widget whose metric's
// audit query errors fails alone, instead of blanking every sibling that shares
// the dashboard's single batched resolution.
function singleWidgetGraph(graph: DataGraph, widget: MetricWidget): DataGraph {
	return {
		events: graph.events,
		metrics: graph.metrics,
		dashboards: [
			{ id: SINGLE_WIDGET_DASHBOARD, layout: "grid", widgets: [widget] },
		],
	};
}

// Render a resolved widget's series in the shape its visualization asks for. A
// series with no points is "no data yet" for every kind — the audit RPC omits a
// bucket rather than emitting a zero, so an empty series means nothing matched,
// not a real zero worth plotting.
function WidgetBody({ widget }: { widget: ResolvedWidget }) {
	const { series, visualization } = widget;
	if (series.points.length === 0) {
		return <p className="text-sm text-muted-foreground">No data yet.</p>;
	}
	switch (visualization) {
		case "line":
		case "area":
			return (
				<LineChart
					points={series.points.map((point) => point.value)}
					className="text-primary/70"
				/>
			);
		case "bar":
			return (
				<BarList
					items={series.points.map((point) => ({
						label: point.key,
						value: point.value,
					}))}
				/>
			);
		case "number":
			return (
				<span className="text-4xl font-bold tabular-nums tracking-tight">
					{series.total.toLocaleString()}
				</span>
			);
		case "table":
			return (
				<table className="w-full text-sm">
					<tbody>
						{series.points.map((point) => (
							<tr key={point.key} className="border-b last:border-0">
								<td className="py-1 text-muted-foreground">{point.key}</td>
								<td className="py-1 text-right font-mono">
									{point.value.toLocaleString()}
								</td>
							</tr>
						))}
					</tbody>
				</table>
			);
		default: {
			// Compile-time exhaustiveness: a new WidgetVisualization must be
			// handled here or this assignment fails to type-check.
			const _exhaustive: never = visualization;
			return _exhaustive;
		}
	}
}

function WidgetCard({
	graph,
	widget,
	solutionId,
	dashboardId,
	orgId,
}: {
	graph: DataGraph;
	widget: MetricWidget;
	solutionId: string;
	dashboardId: string;
	orgId: string;
}) {
	// The graph is part of the key so a solution that redeploys with a changed
	// declaration refetches instead of serving another graph's cached series that
	// happens to share the same solution/dashboard/widget/org ids. Disabled until
	// the org resolves so the pre-org window reads as loading, never as empty.
	const { data, isPending, isError } = useQuery({
		queryKey: [
			"solution-widget",
			solutionId,
			dashboardId,
			widget.id,
			orgId,
			graph,
		],
		queryFn: () =>
			runDashboard(
				sdk.audit,
				singleWidgetGraph(graph, widget),
				SINGLE_WIDGET_DASHBOARD,
				{ orgId },
			).then((resolved) => resolved.widgets[0]),
		enabled: orgId !== "",
	});

	return (
		<Card>
			<CardHeader className="pb-2">
				<CardTitle className="text-base">
					{widget.title ?? widget.metric}
				</CardTitle>
			</CardHeader>
			<CardContent>
				{isError ? (
					<p className="text-sm text-destructive">Unable to load.</p>
				) : isPending || !data ? (
					<Skeleton className="h-24 w-full" />
				) : (
					<WidgetBody widget={data} />
				)}
			</CardContent>
		</Card>
	);
}

function SolutionDashboard({
	graph,
	dashboardId,
	solutionId,
	layout,
	title,
	widgets,
}: {
	graph: DataGraph;
	dashboardId: string;
	solutionId: string;
	layout: "grid" | "stack";
	title?: string;
	widgets: readonly MetricWidget[];
}) {
	const { organizationId } = useAuth();
	const orgId = organizationId ?? "";

	const cards = widgets.map((widget) => (
		<WidgetCard
			key={widget.id}
			graph={graph}
			widget={widget}
			solutionId={solutionId}
			dashboardId={dashboardId}
			orgId={orgId}
		/>
	));

	return (
		<section className="space-y-4">
			{title && (
				<h2 className="text-lg font-semibold tracking-tight">{title}</h2>
			)}
			{layout === "stack" ? (
				<Stack gap={4}>{cards}</Stack>
			) : (
				<Grid cols={2} gap={4}>
					{cards}
				</Grid>
			)}
		</section>
	);
}

/**
 * Renders every dashboard a registered solution declares in its data-graph slot.
 * Each widget resolves against the audit trail through its own query, so one
 * widget's failure never blanks its siblings. A solution ships only the
 * declaration; all charting lives here in the host.
 */
export function SolutionDashboards({
	graph,
	solutionId,
}: {
	graph: DataGraph;
	solutionId: string;
}) {
	return (
		<div className="space-y-8">
			{graph.dashboards.map((dashboard) => (
				<SolutionDashboard
					key={dashboard.id}
					graph={graph}
					dashboardId={dashboard.id}
					solutionId={solutionId}
					layout={dashboard.layout}
					title={dashboard.title}
					widgets={dashboard.widgets}
				/>
			))}
		</div>
	);
}
