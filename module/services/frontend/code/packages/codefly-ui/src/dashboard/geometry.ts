// Pure SVG-path geometry for the dashboard charts, split out from the React
// components so the scaling math is unit-testable without a DOM. Everything is
// laid out inside a fixed-width viewBox and projected through a `Plot` — the
// drawable rectangle left after margins — so the same primitives draw both the
// full-bleed sparkline (symmetric `PAD` gutter) and an axed dashboard widget
// (wider left/bottom gutter for the value and time labels). No React, no DOM.

import type { SeriesPoint } from "./types.js";

export const VIEW_W = 300;
export const VIEW_H = 120;
export const PAD = 6;

/** Inset of the plot area from each edge of the viewBox. */
export interface Margins {
	top: number;
	right: number;
	bottom: number;
	left: number;
}

/** The drawable rectangle inside the viewBox, in viewBox units. */
export interface Plot {
	left: number;
	right: number;
	top: number;
	bottom: number;
}

// The axis-less sparkline gutter: a hair of padding on every side so a
// non-scaling stroke isn't clipped at the frame, matching the pre-axis charts.
export const SPARK_MARGIN: Margins = { top: PAD, right: PAD, bottom: PAD, left: PAD };

// Extra left/bottom gutter an axed chart needs so the y value labels and the x
// time labels have room outside the plot; top/right stay tight.
export const AXIS_MARGIN: Margins = { top: PAD, right: PAD, bottom: 22, left: 34 };

export function plot(width: number, height: number, m: Margins): Plot {
	return { left: m.left, right: width - m.right, top: m.top, bottom: height - m.bottom };
}

/** The full-width sparkline plot at a given height. */
export function sparkPlot(height: number): Plot {
	return plot(VIEW_W, height, SPARK_MARGIN);
}

// The Scale atom: a pure linear map from a domain onto a pixel range. A
// zero-width domain (a flat series) collapses to the range start instead of
// dividing by zero, so it draws one line rather than NaNs.
export function linearScale(d0: number, d1: number, r0: number, r1: number): (v: number) => number {
	const span = d1 - d0 || 1;
	return (v) => r0 + ((v - d0) / span) * (r1 - r0);
}

// Value domain anchored at 0 so a line/area reads against a zero baseline (a
// series that never dips below 0 still shows the floor).
export function valueDomain(values: number[]): [number, number] {
	return [Math.min(0, ...values), Math.max(0, ...values)];
}

// y scale for a series over a plot, anchored at 0. `linePath`/`areaPath` use
// this by default; the axed composition passes a scale built from `niceTicks`
// so the line, the gridlines, and the labels share one mapping.
export function scaleY(values: number[], p: Plot): (v: number) => number {
	const [min, max] = valueDomain(values);
	return linearScale(min, max, p.bottom, p.top);
}

// index → x across the plot. A lone point has no interval to spread over, so it
// sits at the centre (matching the single-point flat line the paths draw).
export function scaleX(index: number, count: number, p: Plot): number {
	if (count <= 1) return (p.left + p.right) / 2;
	return p.left + (index / (count - 1)) * (p.right - p.left);
}

// Round `value` to a "nice" 1/2/5×10ⁿ number; `down` floors to the nearest such
// step (for the tick spacing), otherwise rounds the magnitude up (for the span).
function niceNum(value: number, down: boolean): number {
	if (value <= 0) return 0;
	const base = 10 ** Math.floor(Math.log10(value));
	const frac = value / base;
	let nice: number;
	if (down) nice = frac < 1.5 ? 1 : frac < 3 ? 2 : frac < 7 ? 5 : 10;
	else nice = frac <= 1 ? 1 : frac <= 2 ? 2 : frac <= 5 ? 5 : 10;
	return nice * base;
}

// Evenly-spaced, human-friendly tick values covering [min, max]. The extremes
// are the tick range (not the raw data), so a scale built from the first/last
// tick lines every gridline up with a label. A degenerate span (all zero, or a
// single point) falls back to a 0..1 axis so there is always something to draw.
export function niceTicks(min: number, max: number, count = 4): number[] {
	if (!(max > min)) return [0, 1];
	const step = niceNum(niceNum(max - min, false) / (count - 1), true) || 1;
	const start = Math.floor(min / step) * step;
	const end = Math.ceil(max / step) * step;
	const ticks: number[] = [];
	// `+ step / 2` absorbs float drift so the final tick isn't dropped.
	for (let v = start; v <= end + step / 2; v += step) ticks.push(Number(v.toFixed(6)));
	return ticks;
}

// Build the line `d` for a series across the plot. A lone point has no segment
// to draw from a single move-to, so it becomes a flat line across the full width
// — the card shows the datum exists instead of an empty box (the audit RPC omits
// empty buckets, so a one-bucket series is a real, common state). `y` defaults to
// the anchored scale; the axed charts pass their tick-based scale so the geometry
// matches the drawn axis. `singleY` places that lone flat line: the sparkline
// centres it (its auto-scale would otherwise pin a single value to an edge), while
// an axed chart passes `y(value)` so the line reads true against its y axis.
export function linePath(
	points: SeriesPoint[],
	p: Plot,
	y = scaleY(points.map((pt) => pt.value), p),
	singleY = (p.top + p.bottom) / 2,
): string {
	if (points.length === 0) return "";
	if (points.length === 1) {
		const yy = singleY.toFixed(1);
		return `M ${p.left.toFixed(1)} ${yy} L ${p.right.toFixed(1)} ${yy}`;
	}
	return points
		.map((pt, i) => `${i === 0 ? "M" : "L"} ${scaleX(i, points.length, p).toFixed(1)} ${y(pt.value).toFixed(1)}`)
		.join(" ");
}

// Build the filled-area `d`: the line, dropped to the baseline at both ends and
// closed. Shares `linePath` so the single-point flat line becomes a filled band
// rather than the degenerate zero-width sliver a lone move-to produced.
export function areaPath(
	points: SeriesPoint[],
	p: Plot,
	y = scaleY(points.map((pt) => pt.value), p),
	singleY = (p.top + p.bottom) / 2,
): string {
	if (points.length === 0) return "";
	const baseline = p.bottom.toFixed(1);
	const left = (points.length === 1 ? p.left : scaleX(0, points.length, p)).toFixed(1);
	const right = (points.length === 1 ? p.right : scaleX(points.length - 1, points.length, p)).toFixed(1);
	return `${linePath(points, p, y, singleY)} L ${right} ${baseline} L ${left} ${baseline} Z`;
}
