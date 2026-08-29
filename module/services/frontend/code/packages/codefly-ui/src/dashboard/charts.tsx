// Dependency-free SVG charts for the dashboard kit. Each takes already-resolved
// numeric points and draws a fluid-width, fixed-viewBox chart that colors from
// the CSS `--primary` token, so a dashboard accent themes every chart with no
// prop threading. No charting library, no data fetching — pure presentation.

import type * as React from "react";
import { cn } from "./cn.js";
import type { SeriesPoint } from "./types.js";

const VIEW_W = 300;
const VIEW_H = 120;
const PAD = 6;

// Map values into the viewBox's y-range, guarding the flat-series case (all
// equal) so a single horizontal line sits centered rather than at the edge.
function scaleY(values: number[]): (v: number) => number {
	const max = Math.max(0, ...values);
	const min = Math.min(0, ...values);
	const span = max - min || 1;
	return (v) => VIEW_H - PAD - ((v - min) / span) * (VIEW_H - 2 * PAD);
}

function xAt(index: number, count: number): number {
	if (count <= 1) return VIEW_W / 2;
	return PAD + (index / (count - 1)) * (VIEW_W - 2 * PAD);
}

interface ChartProps {
	points: SeriesPoint[];
	className?: string;
	height?: number;
}

export function LineChart({ points, className, height = VIEW_H }: ChartProps) {
	const values = points.map((p) => p.value);
	const y = scaleY(values);
	const d = points
		.map((p, i) => `${i === 0 ? "M" : "L"} ${xAt(i, points.length).toFixed(1)} ${y(p.value).toFixed(1)}`)
		.join(" ");
	return (
		<svg
			className={cn("w-full", className)}
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio="none"
			role="img"
			aria-label="line chart"
		>
			<path d={d} fill="none" stroke="var(--primary)" strokeWidth={2} vectorEffect="non-scaling-stroke" />
		</svg>
	);
}

export function AreaChart({ points, className, height = VIEW_H }: ChartProps) {
	const values = points.map((p) => p.value);
	const y = scaleY(values);
	const line = points
		.map((p, i) => `${i === 0 ? "M" : "L"} ${xAt(i, points.length).toFixed(1)} ${y(p.value).toFixed(1)}`)
		.join(" ");
	const area = `${line} L ${xAt(points.length - 1, points.length).toFixed(1)} ${height - PAD} L ${xAt(0, points.length).toFixed(1)} ${height - PAD} Z`;
	return (
		<svg
			className={cn("w-full", className)}
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio="none"
			role="img"
			aria-label="area chart"
		>
			<path d={area} fill="var(--primary)" fillOpacity={0.15} stroke="none" />
			<path d={line} fill="none" stroke="var(--primary)" strokeWidth={2} vectorEffect="non-scaling-stroke" />
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
