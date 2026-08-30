// Dependency-free SVG charts for the dashboard kit. Each takes already-resolved
// numeric points and draws a fluid-width, fixed-viewBox chart. Line and area
// stroke with `currentColor`, so a caller sets the hue with a text-color class
// (and a dashboard accent flows through the `--primary`-backed `text-primary`);
// bars and the sparkline default to the `--primary` token. No charting library,
// no data fetching — pure presentation.

import type * as React from "react";
import { cn } from "./cn.js";
import { areaPath, linePath, VIEW_H, VIEW_W } from "./geometry.js";
import type { SeriesPoint } from "./types.js";

interface ChartProps {
	points: SeriesPoint[];
	className?: string;
	height?: number;
}

export function LineChart({ points, className, height = VIEW_H }: ChartProps) {
	const d = linePath(points, height);
	return (
		<svg
			className={cn("w-full", className)}
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio="none"
			role="img"
			aria-label="line chart"
		>
			<path d={d} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
		</svg>
	);
}

export function AreaChart({ points, className, height = VIEW_H }: ChartProps) {
	const area = areaPath(points, height);
	const line = linePath(points, height);
	return (
		<svg
			className={cn("w-full", className)}
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio="none"
			role="img"
			aria-label="area chart"
		>
			<path d={area} fill="currentColor" fillOpacity={0.15} stroke="none" />
			<path d={line} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
		</svg>
	);
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

// A single headline number with an inline sparkline — the KPI tile.
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
