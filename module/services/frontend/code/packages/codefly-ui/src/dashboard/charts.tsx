// Dependency-free SVG charts for the dashboard kit. Each takes already-resolved
// numeric points and draws a fluid-width, fixed-viewBox chart. Line and area
// stroke with `currentColor`, so a caller sets the hue with a text-color class
// (and a dashboard accent flows through the `--primary`-backed `text-primary`);
// bars and the sparkline default to the `--primary` token. No charting library,
// no data fetching — pure presentation.
//
// LineChart/AreaChart are thin compositions of the chart atoms (Svg + Scale +
// Axis + path). By default they draw axis-less — the sparkline floor, so a
// StatChart tile stays clean — and opt into real, tokens-driven axes via the
// `axes` prop, which a dashboard widget turns on.

import type * as React from "react";
import { Axis, Gridline, Svg } from "./atoms.js";
import { cn } from "./cn.js";
import {
	areaPath,
	AXIS_MARGIN,
	linearScale,
	linePath,
	niceTicks,
	PAD,
	plot,
	sparkPlot,
	valueDomain,
	VIEW_H,
	VIEW_W,
} from "./geometry.js";
import type { SeriesPoint } from "./types.js";

// Opt-in axes: `true` for both, or per-axis toggles (each defaults on when the
// object form is used, so `{ y: false }` keeps only the x/time axis).
export type ChartAxes = boolean | { x?: boolean; y?: boolean };

function resolveAxes(axes: ChartAxes | undefined): { x: boolean; y: boolean } {
	if (!axes) return { x: false, y: false };
	if (axes === true) return { x: true, y: true };
	return { x: axes.x ?? true, y: axes.y ?? true };
}

interface ChartProps {
	points: SeriesPoint[];
	className?: string;
	height?: number;
	axes?: ChartAxes;
}

// The shared cartesian body for line and area. `fill` adds the area band under
// the line. With no axes it draws the full-bleed sparkline (unchanged); with an
// axis it grows the matching gutter, builds one tick-based scale, and lines the
// path, gridlines, and labels up against it. A plain function (not a component)
// so LineChart/AreaChart return the Svg tree directly — the path stays a direct
// child, which keeps the chart's drawn geometry inspectable without a DOM.
function cartesian({
	points,
	className,
	height,
	axes,
	fill,
	label,
}: Required<Pick<ChartProps, "points" | "height">> &
	Pick<ChartProps, "className" | "axes"> & { fill: boolean; label: string }) {
	const { x: showX, y: showY } = resolveAxes(axes);

	if (!showX && !showY) {
		const p = sparkPlot(height);
		return (
			<Svg height={height} stretch className={className} ariaLabel={label}>
				{fill && <path d={areaPath(points, p)} fill="currentColor" fillOpacity={0.15} stroke="none" />}
				<path d={linePath(points, p)} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
			</Svg>
		);
	}

	const [min, max] = valueDomain(points.map((pt) => pt.value));
	const ticks = niceTicks(min, max);
	const p = plot(VIEW_W, height, {
		top: PAD,
		right: PAD,
		bottom: showX ? AXIS_MARGIN.bottom : PAD,
		left: showY ? AXIS_MARGIN.left : PAD,
	});
	const y = linearScale(ticks[0], ticks[ticks.length - 1], p.bottom, p.top);
	// A lone bucket draws a flat line; anchor it at its own value so it reads
	// true against the y axis (the sparkline's centred default would misplace it).
	const singleY = points.length === 1 ? y(points[0].value) : undefined;

	return (
		<Svg height={height} stretch={false} className={className} ariaLabel={label}>
			{showY && <Gridline plot={p} ys={ticks.map(y)} />}
			{fill && <path d={areaPath(points, p, y, singleY)} fill="currentColor" fillOpacity={0.15} stroke="none" />}
			<path d={linePath(points, p, y, singleY)} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
			<Axis
				plot={p}
				y={showY ? { ticks, scale: y } : undefined}
				x={showX ? { keys: points.map((pt) => pt.key) } : undefined}
			/>
		</Svg>
	);
}

export function LineChart({ points, className, height = VIEW_H, axes }: ChartProps) {
	return cartesian({ points, className, height, axes, fill: false, label: "line chart" });
}

export function AreaChart({ points, className, height = VIEW_H, axes }: ChartProps) {
	return cartesian({ points, className, height, axes, fill: true, label: "area chart" });
}

// A ranked horizontal bar list — the categorical counterpart to the line/area
// charts. Widths are relative to the largest value; labels are the group keys.
export function BarList({
	points,
	className,
	format,
}: {
	points: SeriesPoint[];
	className?: string;
	format?: (key: string) => string;
}) {
	const max = Math.max(1, ...points.map((p) => p.value));
	return (
		<div className={cn("flex flex-col gap-2", className)}>
			{points.map((p) => (
				<div key={p.key} className="flex items-center gap-2 text-sm">
					<span className="w-32 shrink-0 truncate text-muted-foreground" title={p.key}>
						{format ? format(p.key) : p.key}
					</span>
					<span className="relative h-4 flex-1 overflow-hidden rounded bg-muted">
						<span
							className="absolute inset-y-0 left-0 rounded"
							style={{ width: `${(p.value / max) * 100}%`, backgroundColor: "var(--primary)" }}
						/>
					</span>
					<span className="w-10 shrink-0 text-right tabular-nums">{p.value.toLocaleString()}</span>
				</div>
			))}
		</div>
	);
}

// A single headline number with an inline sparkline — the KPI tile. The
// sparkline stays axis-less by construction (no `axes` prop), so the tile reads
// as one number, not a chart.
export function StatChart({
	total,
	points,
	className,
}: {
	total: number;
	points: SeriesPoint[];
	className?: string;
}) {
	const style: React.CSSProperties = { color: "var(--primary)" };
	return (
		<div className={cn("flex items-end justify-between gap-4", className)}>
			<span className="text-4xl font-bold tabular-nums tracking-tight">{total.toLocaleString()}</span>
			<div className="w-[120px]" style={style}>
				<LineChart points={points} height={40} />
			</div>
		</div>
	);
}
