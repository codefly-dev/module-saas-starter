// Dependency-free chart atoms the line/area charts compose from: the framing
// <Svg>, the background <Gridline> rules, and the <Axis> ticks + labels. Split
// out from `charts.tsx` so axes are a reusable primitive — any current or future
// chart (and any solution) gets the same tokens-driven axes rather than a
// per-chart hand-roll. Everything colours from theme tokens (border,
// muted-foreground), never a hardcoded hue, so it themes and darks for free.

import type * as React from "react";
import { cn } from "./cn.js";
import { formatAxisKey, formatAxisValue } from "./format.js";
import { type Plot, scaleX, VIEW_W } from "./geometry.js";

// At most this many x labels fit the fixed-width viewBox without colliding;
// denser series show a strided subset (always including the last bucket).
const MAX_X_LABELS = 6;

// The fluid-width, fixed-viewBox frame. A sparkline `stretch`es to fill its box
// (`preserveAspectRatio="none"`, as the axis-less charts always have); an axed
// chart keeps a uniform scale so its tick text isn't distorted.
export function Svg({
	height,
	stretch,
	className,
	ariaLabel,
	children,
}: {
	height: number;
	stretch: boolean;
	className?: string;
	ariaLabel: string;
	children: React.ReactNode;
}) {
	return (
		<svg
			className={cn("w-full", className)}
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio={stretch ? "none" : "xMidYMid meet"}
			role="img"
			aria-label={ariaLabel}
		>
			{children}
		</svg>
	);
}

// Horizontal background rules at each y position — the visual counterpart to the
// y axis. Optional: a chart draws them only when it wants the reference grid.
export function Gridline({ plot, ys }: { plot: Plot; ys: number[] }) {
	return (
		<g aria-hidden>
			{ys.map((y) => (
				<line
					key={y}
					x1={plot.left}
					x2={plot.right}
					y1={y}
					y2={y}
					className="stroke-border"
					strokeWidth={1}
					vectorEffect="non-scaling-stroke"
				/>
			))}
		</g>
	);
}

export interface YAxis {
	ticks: number[];
	scale: (value: number) => number;
	format?: (value: number) => string;
}

export interface XAxis {
	keys: string[];
	format?: (key: string) => string;
}

// Even indices across `count` so no more than `max` labels show, always keeping
// the last bucket so the axis ends on the latest data.
function stride(count: number, max: number): Set<number> {
	if (count <= max) return new Set(Array.from({ length: count }, (_, i) => i));
	const step = Math.ceil(count / max);
	const shown = new Set<number>();
	for (let i = 0; i < count; i += step) shown.add(i);
	shown.add(count - 1);
	return shown;
}

// Ticks and labels for either or both axes, drawn against the plot. The x axis
// time-formats ISO bucket keys by default (see `formatAxisKey`); both formatters
// are overridable. Text colours from `muted-foreground` so it reads as chrome,
// not data.
export function Axis({ plot, x, y }: { plot: Plot; x?: XAxis; y?: YAxis }) {
	const formatValue = y?.format ?? formatAxisValue;
	const formatKey = x?.format ?? formatAxisKey;
	const shown = x ? stride(x.keys.length, MAX_X_LABELS) : null;
	return (
		<g aria-hidden>
			{y?.ticks.map((tick) => (
				<text
					key={tick}
					x={plot.left - 6}
					y={y.scale(tick)}
					textAnchor="end"
					dominantBaseline="middle"
					className="fill-muted-foreground text-[10px] tabular-nums"
				>
					{formatValue(tick)}
				</text>
			))}
			{x?.keys.map((key, i) =>
				shown?.has(i) ? (
					<text
						key={key}
						x={scaleX(i, x.keys.length, plot)}
						y={plot.bottom + 14}
						textAnchor="middle"
						className="fill-muted-foreground text-[10px]"
					>
						{formatKey(key)}
					</text>
				) : null,
			)}
		</g>
	);
}
