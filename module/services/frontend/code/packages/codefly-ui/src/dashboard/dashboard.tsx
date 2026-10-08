"use client";

// The shared, pure <Dashboard> renderer. It takes a fully-resolved DashboardView
// — widgets already bound to their series — and paints it. It fetches nothing,
// reads no host context, and imports no app code, so the host app and a
// solution's Module-Federation remote render identical dashboards from the same
// package instance. Data resolution (metric → audit query) is the job of
// `@codefly-dev/saas-sdk`'s `runDashboard`; use `fromDashboardData` to bridge.
//
// It paints with the kit's METRIC tier — `StatTile`/`KPIRow` for scalars,
// `metric-chart`'s line/area/bar over `ChartSeries` for series — which is the
// tier the host's own operations pages are drawn with. Before, this renderer
// and the host's solution dashboard both used the older `charts.tsx` tier, so a
// solution's Dashboard tab read plainer than the host's own pages from the same
// kit, and there were two renderers to keep in step rather than one.

import type * as React from "react";
import { Fragment, type ReactNode } from "react";
import { Card } from "../layout/card.js";
import { Section } from "../layout/page.js";
import { Skeleton } from "../layout/skeleton.js";
import { cn } from "./cn.js";
import { parseTimeKey } from "./format.js";
import {
	AreaChart as MetricAreaChart,
	BarChart as MetricBarChart,
	LineChart as MetricLineChart,
} from "./metric-chart.js";
import type { ChartSeries } from "./metric-geometry.js";
import { type MetricState, MetricStateBadge } from "./metric-state.js";
import { KPIRow, type Metric, StatTile } from "./metric-tiles.js";
import type {
	DashboardView,
	DashboardWidgetView,
	SeriesPoint,
	WidgetSeries,
} from "./types.js";

// Column-span utility classes, matching the responsive grid below so a spanning
// card widens in step with it. A span is clamped to the grid's column count by
// the caller before lookup.
const COL_SPAN: Record<1 | 2 | 3 | 4, string> = {
	1: "",
	2: "sm:col-span-2",
	3: "sm:col-span-2 lg:col-span-3",
	4: "sm:col-span-2 lg:col-span-4",
};

const GRID_COLS: Record<1 | 2 | 3 | 4, string> = {
	1: "grid-cols-1",
	2: "grid-cols-1 sm:grid-cols-2",
	3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
	4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
};

/** The widget's own title, or its id when the declaration gave none. */
function widgetTitle(widget: DashboardWidgetView): string {
	return widget.title ?? widget.id;
}

/**
 * Whether the series is bucketed over time, so its points are a trend rather
 * than a set of categories. Only a time-bucketed series earns a sparkline and
 * a bucket-over-bucket delta: drawing a line through counts grouped by event
 * type or region would assert an order and a direction the data does not have.
 */
function isTimeBucketed(points: SeriesPoint[]): boolean {
	return points.length > 1 && points.every((p) => parseTimeKey(p.key) !== null);
}

/**
 * The badge a resolved series earns, or `undefined` when it is simply ready.
 *
 * `partial` outranks a withheld total because it is the *reason* the total is
 * withheld (a partial series keeps its observed points and gives up its total),
 * and a reader who sees "No total" alone learns less than one who sees
 * "Partial data". An empty series is `no_data`, never a zero: the audit
 * aggregate omits a bucket rather than emitting one, so no points means nothing
 * matched.
 */
function seriesState(series: WidgetSeries): MetricState | undefined {
	if (series.coverage === "partial") return "partial";
	if (series.coverage === "empty" || series.points.length === 0) {
		return "no_data";
	}
	if (series.total === null) return "no_total";
	return undefined;
}

/**
 * Change from the second-to-last bucket to the last, as the signed fraction
 * `StatTile` renders. A change from zero is not a percentage, so it is no
 * delta at all rather than an infinite one.
 */
function lastBucketDelta(points: SeriesPoint[]): number | undefined {
	if (points.length < 2) return undefined;
	const previous = points[points.length - 2].value;
	const latest = points[points.length - 1].value;
	if (previous === 0) return undefined;
	return (latest - previous) / Math.abs(previous);
}

/** A resolved scalar widget as the `Metric` a tile binds to. */
function widgetMetric(
	widget: DashboardWidgetView,
	series: WidgetSeries,
): Metric {
	const trend = isTimeBucketed(series.points);
	const delta = trend ? lastBucketDelta(series.points) : undefined;
	return {
		id: widget.id,
		label: widgetTitle(widget),
		// A withheld total is passed through as null, so the tile shows a dash
		// and its badge says what withheld it. Coercing it to 0 here would
		// report "nothing happened" where the pipeline said "cannot say".
		value: series.total,
		format: widget.format,
		unit: widget.unit,
		higherIsBetter: widget.higherIsBetter,
		series: trend ? series.points.map((p) => p.value) : undefined,
		delta,
		// Only named when there is a delta: `MetricDelta` reads a label with no
		// delta as "no data <label>", which is a different (and here untrue)
		// statement about the comparison period.
		deltaLabel: delta === undefined ? undefined : "vs previous bucket",
		state: seriesState(series),
	};
}

/** One series on the shared label axis the metric charts consume. */
function chartSeries(
	widget: DashboardWidgetView,
	series: WidgetSeries,
): ChartSeries[] {
	return [
		{
			name: widgetTitle(widget),
			data: series.points.map((p) => ({ label: p.key, value: p.value })),
		},
	];
}

/** The resolved series drawn the way the widget's visualization asks. */
function WidgetBody({
	widget,
	series,
}: {
	widget: DashboardWidgetView;
	series: WidgetSeries;
}) {
	const title = widgetTitle(widget);
	switch (widget.visualization) {
		case "line":
			return (
				<MetricLineChart series={chartSeries(widget, series)} title={title} />
			);
		case "area":
			return (
				<MetricAreaChart series={chartSeries(widget, series)} title={title} />
			);
		case "bar":
			return (
				<MetricBarChart series={chartSeries(widget, series)} title={title} />
			);
		case "table":
			return series.points.length === 0 ? (
				<p className="py-6 type-body text-muted-foreground">No data yet.</p>
			) : (
				<div className="overflow-x-auto">
					<table className="w-full type-body">
						<caption className="sr-only">{title}</caption>
						<tbody>
							{series.points.map((p) => (
								<tr key={p.key} className="border-b last:border-0">
									<td className="py-1 pr-4 text-muted-foreground">{p.key}</td>
									<td className="py-1 text-right tabular-nums">
										{p.value.toLocaleString()}
									</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			);
		default:
			return null;
	}
}

export interface DashboardWidgetProps {
	widget: DashboardWidgetView;
	/**
	 * Controls for this widget's header, opposite its title — a drag handle, a
	 * remove button, a panel that says where the number comes from. The
	 * renderer never invents them; a dashboard with nothing to do to a tile
	 * passes none.
	 */
	actions?: ReactNode;
	className?: string;
}

/**
 * One widget of a dashboard, drawn with the metric tier: a scalar as a
 * `StatTile` (value, format, a sparkline and a bucket delta when the metric is
 * bucketed over time, and a `MetricStateBadge` from the series' coverage), a
 * series as a metric chart inside a `Card`.
 *
 * Exported, and not merely an internal of {@link Dashboard}, because a host
 * that resolves each widget through its own query — so one widget's failure
 * cannot blank its siblings — must render each widget in its own component.
 * React fixes the number of hooks a component may call, so those queries
 * cannot be hoisted into one renderer that takes the whole view. Compose this
 * from such a component; never redraw it.
 */
export function DashboardWidget({
	widget,
	actions,
	className,
}: DashboardWidgetProps) {
	const { series } = widget;
	const title = widgetTitle(widget);

	// `failed` is read BEFORE the series, not after: a failed resolution leaves
	// no series behind, so testing for the series first would draw every
	// failure as a wait that never ends.
	if (widget.visualization === "number") {
		const metric: Metric = widget.failed
			? {
					id: widget.id,
					label: title,
					value: null,
					state: "provider_unavailable",
				}
			: series === null
				? { id: widget.id, label: title, value: null, state: "loading" }
				: widgetMetric(widget, series);
		return <StatTile metric={metric} actions={actions} className={className} />;
	}

	// A chart or table card carries its coverage badge in the card's own
	// description slot — the line under the title that says what this card is —
	// so a reader meets "Partial data" where they meet the title, as they do on
	// a tile, instead of as a sentence pushed above the geometry.
	const state =
		widget.failed || series === null ? undefined : seriesState(series);
	return (
		<Card
			title={title}
			description={state ? <MetricStateBadge state={state} /> : undefined}
			actions={actions}
			className={className}
		>
			{widget.failed ? (
				<p className="py-6 type-body text-destructive">Unable to load.</p>
			) : series === null ? (
				<Skeleton className="h-24 w-full" />
			) : (
				<WidgetBody widget={widget} series={series} />
			)}
		</Card>
	);
}

/** A widget and the node drawn for it, for a caller laying the tiles out itself. */
export interface DashboardTile {
	id: string;
	/**
	 * The widget this tile draws. A layout that needs a second copy of a tile —
	 * a drag overlay, say — draws it from here rather than reusing the node,
	 * which is already mounted in the grid.
	 */
	widget: DashboardWidgetView;
	/** The widget as drawn: the kit's card, or `slots.renderWidget`'s. */
	node: ReactNode;
}

/**
 * Where a host composes its own features *around* this renderer, without
 * taking over the drawing. Every slot is optional; a dashboard that needs none
 * of them passes no `slots` at all and gets the full default rendering.
 */
export interface DashboardSlots {
	/** Controls in the dashboard's own header, opposite its title. */
	actions?: ReactNode;
	/**
	 * Draws one widget in place of {@link DashboardWidget}. For a host that
	 * resolves each widget in its own query: return a component that resolves
	 * the series and then mounts `DashboardWidget` with it.
	 */
	renderWidget?: (widget: DashboardWidgetView) => ReactNode;
	/**
	 * Lays the tiles out in place of the default grid or stack — for a grid
	 * whose tiles the viewer can reorder. Tiles arrive in the view's order.
	 */
	layout?: (tiles: DashboardTile[]) => ReactNode;
	/** Shown in place of the layout when the view holds no widgets. */
	empty?: ReactNode;
}

/**
 * Runs of adjacent widgets that are laid out together: consecutive scalars
 * become one `KPIRow`, everything else a grid of cards.
 *
 * Declared order is never rearranged — only adjacent scalars are gathered — so
 * the page shows what the declaration says, in the order it says it. Hoisting
 * every scalar to the top would read better on some dashboards and would also
 * silently overrule a declaration the renderer does not own.
 */
function layoutRuns(tiles: DashboardTile[]): DashboardTile[][] {
	const runs: DashboardTile[][] = [];
	for (const tile of tiles) {
		const scalar = tile.widget.visualization === "number";
		const previous = runs[runs.length - 1];
		const sameKind =
			previous && (previous[0].widget.visualization === "number") === scalar;
		if (sameKind) previous.push(tile);
		else runs.push([tile]);
	}
	return runs;
}

function Tiles({ tiles }: { tiles: DashboardTile[] }) {
	return (
		<>
			{tiles.map((tile) => (
				<Fragment key={tile.id}>{tile.node}</Fragment>
			))}
		</>
	);
}

function DefaultLayout({
	tiles,
	columns,
	grid,
}: {
	tiles: DashboardTile[];
	columns: 1 | 2 | 3 | 4;
	grid: boolean;
}) {
	if (!grid) {
		return (
			<div className="flex flex-col gap-4">
				<Tiles tiles={tiles} />
			</div>
		);
	}
	return (
		<div className="space-y-4">
			{layoutRuns(tiles).map((run) =>
				run[0].widget.visualization === "number" ? (
					<KPIRow key={run[0].id}>
						<Tiles tiles={run} />
					</KPIRow>
				) : (
					<div key={run[0].id} className={cn("grid gap-4", GRID_COLS[columns])}>
						<Tiles tiles={run} />
					</div>
				),
			)}
		</div>
	);
}

/**
 * Render a resolved dashboard. Pass a `DashboardView` (from your own data or via
 * `fromDashboardData(runDashboard(...))`). `accent` overrides the `--primary`
 * token for this dashboard's subtree only, so every chart picks it up.
 */
export function Dashboard({
	data,
	className,
	slots,
}: {
	data: DashboardView;
	className?: string;
	slots?: DashboardSlots;
}) {
	const columns = data.columns ?? 2;
	const style = data.accent
		? ({ "--primary": data.accent } as React.CSSProperties)
		: undefined;
	const isGrid = (data.layout ?? "grid") === "grid";

	const tiles: DashboardTile[] = data.widgets.map((widget) => ({
		id: widget.id,
		widget,
		node: slots?.renderWidget?.(widget) ?? (
			// A scalar tile sits in a KPIRow, whose own geometry sets its width,
			// so a span only applies to a card in the grid.
			<DashboardWidget
				widget={widget}
				className={
					isGrid && widget.visualization !== "number" && widget.span
						? COL_SPAN[Math.min(widget.span, columns) as 1 | 2 | 3 | 4]
						: undefined
				}
			/>
		),
	}));

	return (
		<Section
			title={data.title}
			description={data.description}
			actions={slots?.actions}
			className={className}
			style={style}
		>
			{tiles.length === 0 ? (
				(slots?.empty ?? (
					<p className="type-body text-muted-foreground">No widgets.</p>
				))
			) : slots?.layout ? (
				slots.layout(tiles)
			) : (
				<DefaultLayout tiles={tiles} columns={columns} grid={isGrid} />
			)}
		</Section>
	);
}
