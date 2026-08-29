"use client";

import type { DataGraph } from "@codefly/saas-plugin-manifest";
import {
	createSaasClient,
	type DashboardData,
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

function DashboardView({ data }: { data: DashboardData }) {
	const cards = data.widgets.map((widget) => (
		<Card key={widget.id}>
			<CardHeader className="pb-2">
				<CardTitle className="text-base">
					{widget.title ?? widget.metricId}
				</CardTitle>
			</CardHeader>
			<CardContent>
				<WidgetBody widget={widget} />
			</CardContent>
		</Card>
	));
	return data.layout === "stack" ? (
		<Stack gap={4}>{cards}</Stack>
	) : (
		<Grid cols={2} gap={4}>
			{cards}
		</Grid>
	);
}

function SolutionDashboard({
	graph,
	dashboardId,
	solutionId,
	title,
}: {
	graph: DataGraph;
	dashboardId: string;
	solutionId: string;
	title?: string;
}) {
	const { organizationId } = useAuth();
	const orgId = organizationId ?? "";

	// The graph is fixed per registration, so the org is the only dimension that
	// varies; keying on it (plus the solution + dashboard) scopes the cache to the
	// viewer and refetches when they switch orgs. Disabled until the org resolves
	// so the pre-org window reads as loading, never as an empty dashboard.
	const { data, isPending, isError } = useQuery({
		queryKey: ["solution-dashboard", solutionId, dashboardId, orgId],
		queryFn: () => runDashboard(sdk.audit, graph, dashboardId, { orgId }),
		enabled: orgId !== "",
	});

	return (
		<section className="space-y-4">
			{title && (
				<h2 className="text-lg font-semibold tracking-tight">{title}</h2>
			)}
			{isError ? (
				<p className="text-sm text-destructive">
					Unable to load this dashboard.
				</p>
			) : isPending || !data ? (
				<Grid cols={2} gap={4}>
					<Skeleton className="h-40 w-full" />
					<Skeleton className="h-40 w-full" />
				</Grid>
			) : (
				<DashboardView data={data} />
			)}
		</section>
	);
}

/**
 * Renders every dashboard a registered solution declares in its data-graph slot,
 * each resolved independently against the audit trail. A solution ships only the
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
					title={dashboard.title}
				/>
			))}
		</div>
	);
}
