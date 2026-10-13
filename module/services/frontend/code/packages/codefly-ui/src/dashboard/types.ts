// Presentational view model for the shared <Dashboard> renderer. It is
// deliberately decoupled from any data runtime: the renderer takes *already
// resolved* series, so it never fetches, never reaches for host hooks, and can
// run identically in the host app and in a solution's Module-Federation remote.
//
// `@codefly-dev/saas-sdk`'s `runDashboard` produces `DashboardData`/`ResolvedWidget`,
// which map onto these shapes one-to-one (see `fromDashboardData`); keeping our
// own types here is what lets `@codefly-dev/ui` stay a pure component library with no
// dependency on the SDK's transport stack.

import type { MetricFormat } from "./metric-tiles.js";

/**
 * How a widget's read ended when it did not succeed. Presentational: a caller
 * classifies its own transport (the host app maps its `ReadOutcome` onto this)
 * and the renderer only decides what to draw.
 */
export type WidgetReadFailure = "failed" | "refused";

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
	total: number | null;
	coverage?: "complete" | "partial" | "empty";
}

/** A widget bound to its resolved series. */
export interface DashboardWidgetView {
	id: string;
	visualization: WidgetVisualization;
	title?: string;
	/**
	 * The resolved series, or `null` while it is still being resolved. A
	 * renderer draws a placeholder for `null` rather than an empty series,
	 * because "not yet" and "nothing matched" are different answers and the
	 * second one is a fact about the data.
	 */
	series: WidgetSeries | null;
	/**
	 * How the read ended, when it did not succeed. A caller that resolves each
	 * widget separately sets it per widget, so one failure says so in its own
	 * card instead of blanking its siblings.
	 *
	 * The two are not a severity ordering, they are different questions.
	 * `"failed"` says the read did not complete and says nothing about
	 * authority, so a series already in hand is still the best answer and is
	 * drawn as stale. `"refused"` says this viewer is not allowed to see the
	 * number, which makes a previously fetched one a WRONG answer rather than
	 * an old one — so the renderer discards it, whatever the caller passed.
	 * A boolean cannot express that difference, which is why this is not one.
	 */
	failure?: WidgetReadFailure;
	/** Grid columns this widget spans (1–4); ignored in a stack. */
	span?: 1 | 2 | 3 | 4;
	/**
	 * The id of the {@link DashboardSectionView} this widget is drawn in. A
	 * widget that names none, or a section the view does not hold, is drawn
	 * above every section, untitled.
	 */
	section?: string;
	/**
	 * How the widget's values are written — a scalar's number, a chart's axis,
	 * readout and data table, a table's values. Defaults to a plain number.
	 *
	 * `"percent"` takes a value that is ALREADY a percentage: 45 renders "45%".
	 * It does not take a share — 0.4 renders "0.4%", not "40%". A producer whose
	 * own `percent` means a 0-to-1 share must scale it before it reaches here,
	 * because this field is bound structurally (see `fromDashboardData`) and
	 * nothing on the wire can tell the two conventions apart.
	 * `@codefly-dev/saas-sdk`'s `runDashboard` is such a producer and does that
	 * scaling itself, once: a data graph declares `percent` on a metric whose
	 * value is a share, and the widget it resolves carries percentage points.
	 */
	format?: MetricFormat;
	/** Suffix after a scalar widget's value, e.g. "req/s". */
	unit?: string;
	/** When false a rising value reads as bad (an error count). Defaults true. */
	higherIsBetter?: boolean;
}

/**
 * A titled group of a dashboard's widgets. Sections are drawn in the order the
 * view lists them, each with the widgets that name it, and a section no widget
 * names is still drawn, empty.
 */
export interface DashboardSectionView {
	id: string;
	title: string;
	description?: string;
}

/** The fully-resolved dashboard the renderer paints. */
export interface DashboardView {
	title?: string;
	description?: string;
	layout?: DashboardLayoutKind;
	/**
	 * The sections the widgets are grouped into, in the order they are drawn.
	 * A view without sections draws its widgets as one untitled group.
	 */
	sections?: DashboardSectionView[];
	/** Grid column count (default 2); ignored in a stack. */
	columns?: 1 | 2 | 3 | 4;
	/** Optional accent (any CSS color) applied to charts via the primary token. */
	accent?: string;
	widgets: DashboardWidgetView[];
}

// The minimal shape of `@codefly-dev/saas-sdk`'s runDashboard result. Declared
// structurally (not imported) so this package keeps zero runtime deps; any value
// with these fields — including the SDK's `DashboardData` — satisfies it.
interface ResolvedWidgetLike {
	id: string;
	visualization: WidgetVisualization;
	title?: string;
	series: WidgetSeries;
	section?: string;
	format?: MetricFormat;
	unit?: string;
	higherIsBetter?: boolean;
}
interface DashboardDataLike {
	title?: string;
	layout?: DashboardLayoutKind;
	sections?: readonly DashboardSectionView[];
	widgets: ResolvedWidgetLike[];
}

/**
 * Adapt a resolved data-graph dashboard (`@codefly-dev/saas-sdk` `runDashboard`)
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
		sections: data.sections?.map((section) => ({ ...section })),
		widgets: data.widgets.map((widget) => ({
			id: widget.id,
			visualization: widget.visualization,
			title: widget.title,
			series: widget.series,
			section: widget.section,
			format: widget.format,
			unit: widget.unit,
			higherIsBetter: widget.higherIsBetter,
		})),
	};
}
