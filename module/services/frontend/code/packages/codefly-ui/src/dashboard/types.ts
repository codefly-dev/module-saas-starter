// Presentational view model for the shared <Dashboard> renderer. It is
// deliberately decoupled from any data runtime: the renderer takes *already
// resolved* series, so it never fetches, never reaches for host hooks, and can
// run identically in the host app and in a solution's Module-Federation remote.
//
// `@codefly/saas-sdk`'s `runDashboard` produces `DashboardData`/`ResolvedWidget`,
// which map onto these shapes one-to-one (see `fromDashboardData`); keeping our
// own types here is what lets `@codefly/ui` stay a pure component library with no
// dependency on the SDK's transport stack.

/** How a widget draws its series. */
export type WidgetVisualization = "line" | "bar" | "area" | "number" | "table";

/** How a dashboard arranges its widgets. */
export type DashboardLayoutKind = "grid" | "stack";

/** One resolved data point: a group key and its numeric value. */
export interface SeriesPoint {
	key: string;
	value: number;
}

/** A resolved metric series a widget renders. */
export interface WidgetSeries {
	points: SeriesPoint[];
	total: number;
}

/** A widget bound to its resolved series. */
export interface DashboardWidgetView {
	id: string;
	visualization: WidgetVisualization;
	title?: string;
	series: WidgetSeries;
	/** Grid columns this widget spans (1–4); ignored in a stack. */
	span?: 1 | 2 | 3 | 4;
}

/** The fully-resolved dashboard the renderer paints. */
export interface DashboardView {
	title?: string;
	description?: string;
	layout?: DashboardLayoutKind;
	/** Grid column count (default 2); ignored in a stack. */
	columns?: 1 | 2 | 3 | 4;
	/** Optional accent (any CSS color) applied to charts via the primary token. */
	accent?: string;
	widgets: DashboardWidgetView[];
}

// The minimal shape of `@codefly/saas-sdk`'s runDashboard result. Declared
// structurally (not imported) so this package keeps zero runtime deps; any value
// with these fields — including the SDK's `DashboardData` — satisfies it.
interface ResolvedWidgetLike {
	id: string;
	visualization: WidgetVisualization;
	title?: string;
	series: { points: SeriesPoint[]; total: number };
}
interface DashboardDataLike {
	title?: string;
	layout?: DashboardLayoutKind;
	widgets: ResolvedWidgetLike[];
}

/**
 * Adapt a resolved data-graph dashboard (`@codefly/saas-sdk` `runDashboard`)
 * into the renderer's view model. The two shapes already align; this is the
 * one seam a consumer crosses between the data runtime and the component kit.
 */
export function fromDashboardData(
	data: DashboardDataLike,
	extra?: { description?: string; columns?: 1 | 2 | 3 | 4; accent?: string },
): DashboardView {
	return {
		title: data.title,
		description: extra?.description,
		layout: data.layout ?? "grid",
		columns: extra?.columns,
		accent: extra?.accent,
		widgets: data.widgets.map((widget) => ({
			id: widget.id,
			visualization: widget.visualization,
			title: widget.title,
			series: widget.series,
		})),
	};
}
