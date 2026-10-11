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

// The KPI row's own column count, capped at the dashboard's DECLARED one.
// `KPIRow` is four across, which is what headline numbers want; a dashboard
// that declared fewer columns did not ask for more, and one that declared none
// did not ask for two either — so this is applied only when there is a
// declaration to honour. `cn` resolves these against the row's own defaults
// per breakpoint.
const KPI_COLS: Record<1 | 2 | 3 | 4, string> = {
	1: "grid-cols-1 sm:grid-cols-1 lg:grid-cols-1",
	2: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-2",
	3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
	4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
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
 * The series' values in time order, or `null` when it is not a trend at all.
 *
 * Only a series whose every key is a time bucket earns a sparkline and a
 * bucket-over-bucket delta: drawing a line through counts grouped by event type
 * or region would assert an order and a direction the data does not have.
 *
 * The order comes from the parsed timestamps rather than from the array,
 * because nothing guarantees the array is chronological — the resolver appends
 * one point per bucket in whatever order the aggregate returned them, and
 * `ChartSeries` documents x-order as the producer's responsibility. Reading
 * "previous" and "latest" off the last two positions would, for a series
 * delivered newest-first, report the change with the wrong sign — and a delta's
 * sign is drawn as a colour, so a decline would read as a green rise.
 */
function trendValues(points: SeriesPoint[]): number[] | null {
	if (points.length < 2) return null;
	const dated: { at: number; value: number }[] = [];
	for (const point of points) {
		const at = parseTimeKey(point.key);
		if (at === null) return null;
		dated.push({ at: at.getTime(), value: point.value });
	}
	dated.sort((a, b) => a.at - b.at);
	return dated.map((point) => point.value);
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
 * Change from the second-latest bucket to the latest, as the signed fraction
 * `StatTile` renders. A change from zero is not a percentage, so it is no
 * delta at all rather than an infinite one.
 */
function lastBucketDelta(trend: number[]): number | undefined {
	if (trend.length < 2) return undefined;
	const previous = trend[trend.length - 2];
	const latest = trend[trend.length - 1];
	if (previous === 0) return undefined;
	return (latest - previous) / Math.abs(previous);
}

/** A resolved scalar widget as the `Metric` a tile binds to. */
function widgetMetric(
	widget: DashboardWidgetView,
	series: WidgetSeries,
): Metric {
	const trend = trendValues(series.points);
	const delta = trend ? lastBucketDelta(trend) : undefined;
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
		series: trend ?? undefined,
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
		case "number":
			// A scalar is drawn as a tile by DashboardWidget, never as a card
			// body. The case is here so the switch stays exhaustive.
			return null;
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
		default: {
			// Compile-time exhaustiveness: a new WidgetVisualization must be
			// handled here or this assignment fails to type-check. Without it a
			// visualization the metric tier has no mark for draws a titled card
			// with an empty body, which is indistinguishable from a widget whose
			// query returned nothing.
			const unhandled: never = widget.visualization;
			return unhandled;
		}
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

	// Three states, not two. A failure with nothing behind it is a failure; a
	// failure with a series still in hand is a refresh that did not land, and
	// blanking the tile would throw away values that are still the best
	// available. `stale` is the kit's own word for that, and it outranks the
	// coverage badge because it is the more actionable of the two.
	const lost = widget.failed === true && series === null;
	const stale = widget.failed === true && series !== null;

	if (widget.visualization === "number") {
		const metric: Metric = lost
			? {
					id: widget.id,
					label: title,
					value: null,
					state: "provider_unavailable",
				}
			: series === null
				? { id: widget.id, label: title, value: null, state: "loading" }
				: { ...widgetMetric(widget, series), ...(stale && { state: "stale" }) };
		return <StatTile metric={metric} actions={actions} className={className} />;
	}

	// A chart or table card carries its coverage badge in the card's own
	// description slot — the line under the title that says what this card is —
	// so a reader meets "Partial data" where they meet the title, as they do on
	// a tile, instead of as a sentence pushed above the geometry.
	const state: MetricState | undefined =
		series === null ? undefined : stale ? "stale" : seriesState(series);
	return (
		<Card
			title={title}
			description={state ? <MetricStateBadge state={state} /> : undefined}
			actions={actions}
			className={className}
		>
			{lost ? (
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
		const previous = runs[runs.length - 1];
		if (previous && inKpiRow(previous[0].widget) === inKpiRow(tile.widget)) {
			previous.push(tile);
		} else {
			runs.push([tile]);
		}
	}
	return runs;
}

/**
 * Whether this widget belongs in a KPI row rather than in the card grid.
 *
 * A scalar that declares a `span` is asking for a card's width, which the KPI
 * row's own geometry cannot give it, so it stays a grid item and keeps its
 * span. Only a scalar that asked for nothing becomes a headline number.
 */
function inKpiRow(widget: DashboardWidgetView): boolean {
	return widget.visualization === "number" && !widget.span;
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
	declaredColumns,
	grid,
}: {
	tiles: DashboardTile[];
	columns: 1 | 2 | 3 | 4;
	/** `data.columns` as declared, or undefined when the view named none. */
	declaredColumns?: 1 | 2 | 3 | 4;
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
				inKpiRow(run[0].widget) ? (
					<KPIRow
						key={run[0].id}
						className={declaredColumns && KPI_COLS[declaredColumns]}
					>
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
			// A span is a request for grid width, so it applies to anything the
			// grid lays out. A scalar that declares one is kept out of the KPI
			// row for exactly that reason — see `inKpiRow`.
			<DashboardWidget
				widget={widget}
				className={
					isGrid && widget.span
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
				<DefaultLayout
					tiles={tiles}
					columns={columns}
					declaredColumns={data.columns}
					grid={isGrid}
				/>
			)}
		</Section>
	);
}
