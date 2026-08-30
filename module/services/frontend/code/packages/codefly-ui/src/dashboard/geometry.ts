// Pure SVG-path geometry for the dashboard charts, split out from the React
// components so the scaling math is unit-testable without a DOM. Every path is
// built in a fixed-width viewBox whose *height the caller passes in*, so a chart
// drawn at a non-default height (a sparkline) scales into its own box instead of
// a constant one — otherwise its points overflow the viewBox and get clipped.

import type { SeriesPoint } from "./types.js";

export const VIEW_W = 300;
export const VIEW_H = 120;
export const PAD = 6;

// Map values into the given height's y-range, anchored at 0 so a line/area reads
// against a zero baseline. The flat-series case (all equal → span 0) collapses
// to a single line rather than dividing by zero. `height` must be threaded from
// the caller: closing over a constant is exactly what clipped the sparkline.
export function scaleY(values: number[], height: number): (v: number) => number {
	const max = Math.max(0, ...values);
	const min = Math.min(0, ...values);
	const span = max - min || 1;
	return (v) => height - PAD - ((v - min) / span) * (height - 2 * PAD);
}

export function xAt(index: number, count: number): number {
	if (count <= 1) return VIEW_W / 2;
	return PAD + (index / (count - 1)) * (VIEW_W - 2 * PAD);
}

// Build the line `d` for a series at the given height. A lone point has no
// segment to draw from a single move-to, so it becomes a flat horizontal line at
// mid-height across the full width — the card shows the datum exists instead of
// rendering an empty box (the audit RPC omits empty buckets, so a one-bucket
// series is a real, common state).
export function linePath(points: SeriesPoint[], height: number): string {
	if (points.length === 0) return "";
	if (points.length === 1) {
		const yy = (height / 2).toFixed(1);
		return `M ${PAD.toFixed(1)} ${yy} L ${(VIEW_W - PAD).toFixed(1)} ${yy}`;
	}
	const y = scaleY(points.map((p) => p.value), height);
	return points
		.map((p, i) => `${i === 0 ? "M" : "L"} ${xAt(i, points.length).toFixed(1)} ${y(p.value).toFixed(1)}`)
		.join(" ");
}

// Build the filled-area `d`: the line, dropped to the baseline at both ends and
// closed. Shares `linePath` so the single-point flat line becomes a filled band
// rather than the degenerate zero-width sliver a lone move-to produced.
export function areaPath(points: SeriesPoint[], height: number): string {
	if (points.length === 0) return "";
	const baseline = (height - PAD).toFixed(1);
	const left = (points.length === 1 ? PAD : xAt(0, points.length)).toFixed(1);
	const right = (points.length === 1 ? VIEW_W - PAD : xAt(points.length - 1, points.length)).toFixed(1);
	return `${linePath(points, height)} L ${right} ${baseline} L ${left} ${baseline} Z`;
}
