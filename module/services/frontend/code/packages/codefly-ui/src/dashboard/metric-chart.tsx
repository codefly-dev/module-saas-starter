/**
 * Metric charts — line, area, and bar — for the template dashboard.
 *
 * All three consume metric `data` (a shared {@link ChartSeries} shape), never
 * raw RPC rows: a consumer maps an audit/usage aggregate to series once, then
 * drops the chart into a dashboard. They render as pure SVG scaled by a
 * `viewBox`, so they are fluid to their container, colour from the appearance
 * palette (`--chart-1…5`, light/dark aware), and add a pointer/crosshair
 * readout on top.
 *
 * Series are aligned to a shared label axis (the union of every series' labels)
 * by label, not by array index, so charts of separately-aggregated metrics with
 * differing bucket sets stay correct. A single `xs` array — band-based for bars,
 * point-based otherwise — drives the axis, the marks, and the hover overlay, so
 * labels always sit under the geometry they name.
 *
 * Accessibility: the SVG is decorative (`aria-hidden`); identity never rests on
 * colour alone (a legend keys every series for two-or-more, tabular-nums), and
 * the exact numbers live in a visually-hidden data table that is the chart's
 * screen-reader representation.
 */

"use client";

import { type ReactNode, useId, useState } from "react";
import { cn } from "../layout/cn.js";
import { formatAxisKey } from "./format.js";
import {
	areaPath,
	axisPositions,
	type ChartSeries,
	linearScale,
	linePath,
	niceTicks,
	type ResolvedSeries,
	resolveSeries,
	stackedExtent,
	stackSeries,
	unionLabels,
	valuesExtent,
} from "./metric-geometry.js";

const VIEW_W = 640;
const DEFAULT_VIEW_H = 260;
const MARGIN = { top: 12, right: 16, bottom: 28, left: 48 };
const MAX_X_LABELS = 8;
const PALETTE_SIZE = 5;
const MISSING = "—";

const fullFormat = new Intl.NumberFormat("en-US");

/**
 * Categorical series colour in fixed order from the appearance palette. The kit
 * ships five chart tokens; a dashboard with more than five series should
 * aggregate the tail into an "Other" bucket rather than lean on a sixth hue.
 */
export function chartSeriesColor(index: number): string {
	return `var(--chart-${(index % PALETTE_SIZE) + 1})`;
}

export interface MetricChartProps {
	series: ChartSeries[];
	/**
	 * Draw series cumulatively, each band on top of the ones before it, so the
	 * outline is the total and every band is legible. Off, series overlay and
	 * the smaller ones hide behind the larger. Area charts only.
	 */
	stacked?: boolean;
	/** Names the chart for the accessible label; also shown by consumers' card. */
	title: string;
	className?: string;
	/** viewBox height; width is fluid. Defaults to 260. */
	height?: number;
	/** Formats values on the axis, in the tooltip, and in the table. */
	formatValue?: (value: number) => string;
}

interface FrameGeometry {
	viewH: number;
	plotLeft: number;
	plotRight: number;
	plotTop: number;
	plotBottom: number;
	labels: string[];
	resolved: ResolvedSeries[];
	/** X centre of each label — band centres for bars, point scale otherwise. */
	xs: number[];
	ticks: number[];
	scaleY: (value: number) => number;
}

function computeGeometry(
	series: ChartSeries[],
	height: number,
	band: boolean,
	stacked = false,
): FrameGeometry {
	const plotLeft = MARGIN.left;
	const plotRight = VIEW_W - MARGIN.right;
	const plotTop = MARGIN.top;
	const plotBottom = height - MARGIN.bottom;
	const labels = unionLabels(series);
	const resolved = resolveSeries(series, labels);
	// A stacked chart scales to its tallest column, not its tallest series.
	const [min, max] = stacked
		? stackedExtent(stackSeries(resolved))
		: valuesExtent(resolved);
	const ticks = niceTicks(min, max);
	const scaleY = linearScale(
		ticks[0],
		ticks[ticks.length - 1],
		plotBottom,
		plotTop,
	);
	const xs = axisPositions(labels.length, plotLeft, plotRight, band);
	return {
		viewH: height,
		plotLeft,
		plotRight,
		plotTop,
		plotBottom,
		labels,
		resolved,
		xs,
		ticks,
		scaleY,
	};
}

function xTickIndices(count: number): Set<number> {
	if (count <= MAX_X_LABELS) {
		return new Set(Array.from({ length: count }, (_, i) => i));
	}
	const stride = Math.ceil(count / MAX_X_LABELS);
	const shown = new Set<number>();
	for (let i = 0; i < count; i += stride) shown.add(i);
	// The last label always shows. The stride label before it can sit less than
	// one stride away, and two labels that close draw over each other, so the
	// last label takes that one's place.
	const last = count - 1;
	const lastStride = last - (last % stride);
	if (lastStride !== last && lastStride > 0) shown.delete(lastStride);
	shown.add(last);
	return shown;
}

function Legend({
	series,
	marker,
}: {
	series: ResolvedSeries[];
	marker: "line" | "rect";
}) {
	if (series.length < 2) return null;
	return (
		<ul className="flex flex-wrap gap-x-4 gap-y-1">
			{series.map((s, i) => (
				<li
					key={s.name}
					className="flex items-center gap-1.5 type-caption-plain text-muted-foreground"
				>
					<span
						className={cn(
							"inline-block rounded-full",
							marker === "line" ? "h-0.5 w-3" : "h-2.5 w-2.5",
						)}
						style={{ backgroundColor: chartSeriesColor(i) }}
						aria-hidden
					/>
					{s.name}
				</li>
			))}
		</ul>
	);
}

/** The chart's accessible representation: exact values in a real table. */
function DataTable({
	geo,
	title,
	formatValue,
}: {
	geo: FrameGeometry;
	title: string;
	formatValue: (value: number) => string;
}) {
	return (
		<table className="sr-only">
			<caption>{title}</caption>
			<thead>
				<tr>
					<th scope="col">Bucket</th>
					{geo.resolved.map((s) => (
						<th key={s.name} scope="col">
							{s.name}
						</th>
					))}
				</tr>
			</thead>
			<tbody>
				{geo.labels.map((label, row) => (
					<tr key={label}>
						<th scope="row">{label}</th>
						{geo.resolved.map((s) => (
							<td key={s.name}>
								{s.values[row] === null
									? MISSING
									: formatValue(s.values[row] as number)}
							</td>
						))}
					</tr>
				))}
			</tbody>
		</table>
	);
}

function Axes({
	geo,
	formatValue,
}: {
	geo: FrameGeometry;
	formatValue: (value: number) => string;
}) {
	const shown = xTickIndices(geo.labels.length);
	return (
		<>
			{geo.ticks.map((tick) => {
				const y = geo.scaleY(tick);
				return (
					<g key={tick}>
						<line
							x1={geo.plotLeft}
							x2={geo.plotRight}
							y1={y}
							y2={y}
							className="stroke-border"
							strokeWidth={1}
							vectorEffect="non-scaling-stroke"
						/>
						<text
							x={geo.plotLeft - 8}
							y={y}
							textAnchor="end"
							dominantBaseline="middle"
							className="fill-muted-foreground type-chart-label tabular-nums"
						>
							{formatValue(tick)}
						</text>
					</g>
				);
			})}
			{geo.labels.map((label, i) =>
				shown.has(i) ? (
					<text
						key={label}
						x={geo.xs[i]}
						y={geo.plotBottom + 16}
						textAnchor="middle"
						className="fill-muted-foreground type-chart-label"
					>
						{formatAxisKey(label)}
					</text>
				) : null,
			)}
		</>
	);
}

/** Snaps the pointer to the nearest column and reports its index. */
function HoverOverlay({
	geo,
	onSelect,
}: {
	geo: FrameGeometry;
	onSelect: (index: number | null) => void;
}) {
	return (
		<rect
			x={geo.plotLeft}
			y={geo.plotTop}
			width={geo.plotRight - geo.plotLeft}
			height={geo.plotBottom - geo.plotTop}
			fill="transparent"
			onPointerMove={(event) => {
				const rect = event.currentTarget.getBoundingClientRect();
				if (rect.width === 0) return;
				const viewX =
					geo.plotLeft +
					((event.clientX - rect.left) / rect.width) *
						(geo.plotRight - geo.plotLeft);
				let best = 0;
				let bestDistance = Number.POSITIVE_INFINITY;
				for (let i = 0; i < geo.xs.length; i++) {
					const distance = Math.abs(geo.xs[i] - viewX);
					if (distance < bestDistance) {
						bestDistance = distance;
						best = i;
					}
				}
				onSelect(best);
			}}
			onPointerLeave={() => onSelect(null)}
		/>
	);
}

function Tooltip({
	geo,
	index,
	formatValue,
}: {
	geo: FrameGeometry;
	index: number;
	formatValue: (value: number) => string;
}) {
	const leftPercent = (geo.xs[index] / VIEW_W) * 100;
	return (
		<div
			className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md"
			style={{ left: `${leftPercent}%` }}
			aria-hidden
		>
			<div className="mb-1 type-chart-label text-muted-foreground">
				{formatAxisKey(geo.labels[index])}
			</div>
			<ul className="space-y-0.5">
				{geo.resolved.map((s, i) => (
					<li
						key={s.name}
						className="flex items-center gap-2 type-caption-plain"
					>
						<span
							className="inline-block h-0.5 w-3 rounded-full"
							style={{ backgroundColor: chartSeriesColor(i) }}
							aria-hidden
						/>
						<span className="text-muted-foreground">{s.name}</span>
						<span className="ml-auto type-emphasis tabular-nums">
							{s.values[index] === null
								? MISSING
								: formatValue(s.values[index] as number)}
						</span>
					</li>
				))}
			</ul>
		</div>
	);
}

function EmptyChart({
	title,
	height,
	className,
}: {
	title: string;
	height: number;
	className?: string;
}) {
	return (
		<figure
			className={cn("relative w-full", className)}
			style={{ aspectRatio: `${VIEW_W} / ${height}` }}
			aria-label={`${title}: no data`}
		>
			<div className="flex h-full items-center justify-center type-body text-muted-foreground">
				No data
			</div>
		</figure>
	);
}

/**
 * Shared frame for the cartesian charts. `marks` draws the series geometry;
 * `overlay` draws the hover response (crosshair, markers, band highlight) for
 * the active column. Both read positions from `geo.xs`, the single x-axis.
 */
function ChartFrame({
	series,
	title,
	className,
	height = DEFAULT_VIEW_H,
	formatValue = (value) => fullFormat.format(value),
	legendMarker,
	band = false,
	stacked = false,
	marks,
	overlay,
}: MetricChartProps & {
	legendMarker: "line" | "rect";
	band?: boolean;
	marks: (geo: FrameGeometry) => ReactNode;
	overlay: (geo: FrameGeometry, index: number) => ReactNode;
}) {
	const [active, setActive] = useState<number | null>(null);
	const clipId = useId();
	const geo = computeGeometry(series, height, band, stacked);

	if (geo.labels.length === 0) {
		return <EmptyChart title={title} height={height} className={className} />;
	}

	return (
		<figure className={cn("relative w-full", className)}>
			{geo.resolved.length >= 2 && (
				<figcaption className="mb-2">
					<Legend series={geo.resolved} marker={legendMarker} />
				</figcaption>
			)}
			<svg
				viewBox={`0 0 ${VIEW_W} ${height}`}
				className="w-full"
				style={{ aspectRatio: `${VIEW_W} / ${height}` }}
				preserveAspectRatio="xMidYMid meet"
				aria-hidden
			>
				<Axes geo={geo} formatValue={formatValue} />
				{/* The marks are clipped to the plot, padded by an end marker's
				    radius so a point on the plot's edge keeps its whole dot. The
				    extent is computed to contain every point, so this changes
				    nothing when the geometry is right; it is what keeps a wrong
				    extent — or a value a future mark type forgets to include —
				    from drawing over the axes and outside the frame instead of
				    merely being cut off. */}
				<clipPath id={clipId}>
					<rect
						x={geo.plotLeft - MARK_CLIP_PAD}
						y={geo.plotTop - MARK_CLIP_PAD}
						width={geo.plotRight - geo.plotLeft + 2 * MARK_CLIP_PAD}
						height={geo.plotBottom - geo.plotTop + 2 * MARK_CLIP_PAD}
					/>
				</clipPath>
				<g clipPath={`url(#${clipId})`}>{marks(geo)}</g>
				{active !== null && overlay(geo, active)}
				<HoverOverlay geo={geo} onSelect={setActive} />
			</svg>
			{active !== null && (
				<Tooltip geo={geo} index={active} formatValue={formatValue} />
			)}
			<DataTable geo={geo} title={title} formatValue={formatValue} />
		</figure>
	);
}

/** Present points of a resolved series, aligned to the shared x-axis. */
function seriesPoints(values: (number | null)[], geo: FrameGeometry) {
	const points: { x: number; y: number }[] = [];
	values.forEach((v, i) => {
		if (v !== null) points.push({ x: geo.xs[i], y: geo.scaleY(v) });
	});
	return points;
}

function Crosshair({ geo, x }: { geo: FrameGeometry; x: number }) {
	return (
		<line
			x1={x}
			x2={x}
			y1={geo.plotTop}
			y2={geo.plotBottom}
			className="stroke-muted-foreground/40"
			strokeWidth={1}
			vectorEffect="non-scaling-stroke"
		/>
	);
}

const END_MARKER_RADIUS = 3.5;
/** Radius plus the marker's own stroke: what the mark clip must leave room for. */
const MARK_CLIP_PAD = END_MARKER_RADIUS + 1;

function EndMarker({ x, y, color }: { x: number; y: number; color: string }) {
	return (
		<circle
			cx={x}
			cy={y}
			r={END_MARKER_RADIUS}
			fill={color}
			className="stroke-card"
			strokeWidth={2}
			vectorEffect="non-scaling-stroke"
		/>
	);
}

function LineOverlay(geo: FrameGeometry, index: number) {
	return (
		<g>
			<Crosshair geo={geo} x={geo.xs[index]} />
			{geo.resolved.map((s, i) =>
				s.values[index] === null ? null : (
					<EndMarker
						key={s.name}
						x={geo.xs[index]}
						y={geo.scaleY(s.values[index] as number)}
						color={chartSeriesColor(i)}
					/>
				),
			)}
		</g>
	);
}

export function LineChart(props: MetricChartProps) {
	return (
		<ChartFrame
			{...props}
			legendMarker="line"
			marks={(geo) =>
				geo.resolved.map((s, i) => {
					const points = seriesPoints(s.values, geo);
					if (points.length === 0) return <g key={s.name} />;
					const color = chartSeriesColor(i);
					const last = points[points.length - 1];
					return (
						<g key={s.name}>
							<path
								d={linePath(points)}
								fill="none"
								stroke={color}
								strokeWidth={2}
								strokeLinecap="round"
								strokeLinejoin="round"
								vectorEffect="non-scaling-stroke"
							/>
							<EndMarker x={last.x} y={last.y} color={color} />
						</g>
					);
				})
			}
			overlay={LineOverlay}
		/>
	);
}

export function AreaChart(props: MetricChartProps) {
	const { stacked = false } = props;
	return (
		<ChartFrame
			{...props}
			legendMarker="line"
			marks={(geo) => {
				if (stacked) {
					// Bands are drawn bottom-up, each between its own floor (the
					// bands beneath it) and its ceiling. The line traces the
					// ceiling so the topmost line is the total.
					return stackSeries(geo.resolved).map((s, i) => {
						const tops = seriesPoints(s.top, geo);
						const bases = seriesPoints(s.base, geo);
						if (tops.length === 0) return <g key={s.name} />;
						const color = chartSeriesColor(i);
						const last = tops[tops.length - 1];
						return (
							<g key={s.name}>
								<path
									d={bandPath(tops, bases)}
									fill={color}
									fillOpacity={0.28}
								/>
								<path
									d={linePath(tops)}
									fill="none"
									stroke={color}
									strokeWidth={1.5}
									strokeLinejoin="round"
									vectorEffect="non-scaling-stroke"
								/>
								<EndMarker x={last.x} y={last.y} color={color} />
							</g>
						);
					});
				}
				return geo.resolved.map((s, i) => {
					const points = seriesPoints(s.values, geo);
					if (points.length === 0) return <g key={s.name} />;
					const color = chartSeriesColor(i);
					const last = points[points.length - 1];
					return (
						<g key={s.name}>
							<path
								d={areaPath(points, geo.plotBottom)}
								fill={color}
								fillOpacity={0.12}
							/>
							<path
								d={linePath(points)}
								fill="none"
								stroke={color}
								strokeWidth={2}
								strokeLinecap="round"
								strokeLinejoin="round"
								vectorEffect="non-scaling-stroke"
							/>
							<EndMarker x={last.x} y={last.y} color={color} />
						</g>
					);
				});
			}}
			overlay={LineOverlay}
		/>
	);
}

/** A closed region between a ceiling polyline and a floor polyline. */
function bandPath(
	tops: { x: number; y: number }[],
	bases: { x: number; y: number }[],
): string {
	const up = tops
		.map((p, i) => `${i === 0 ? "M" : "L"}${p.x},${p.y}`)
		.join(" ");
	const down = bases
		.slice()
		.reverse()
		.map((p) => `L${p.x},${p.y}`)
		.join(" ");
	return `${up} ${down} Z`;
}

/** Rounds the data-end (top for positive, bottom for negative) of a column. */
function barPath(
	x: number,
	width: number,
	baseY: number,
	valueY: number,
	radius: number,
): string {
	const up = valueY <= baseY;
	const r = Math.min(radius, width / 2, Math.abs(baseY - valueY));
	const right = x + width;
	if (up) {
		return `M${x},${baseY} L${x},${valueY + r} Q${x},${valueY} ${x + r},${valueY} L${right - r},${valueY} Q${right},${valueY} ${right},${valueY + r} L${right},${baseY} Z`;
	}
	return `M${x},${baseY} L${x},${valueY - r} Q${x},${valueY} ${x + r},${valueY} L${right - r},${valueY} Q${right},${valueY} ${right},${valueY - r} L${right},${baseY} Z`;
}

export function BarChart(props: MetricChartProps) {
	return (
		<ChartFrame
			{...props}
			legendMarker="rect"
			band
			marks={(geo) => {
				const seriesCount = geo.resolved.length;
				const slot =
					(geo.plotRight - geo.plotLeft) / Math.max(1, geo.labels.length);
				const group = Math.min(
					slot * 0.7,
					24 * seriesCount + 2 * (seriesCount - 1),
				);
				const gap = seriesCount > 1 ? 2 : 0;
				const barWidth = Math.min(
					24,
					(group - gap * (seriesCount - 1)) / seriesCount,
				);
				const baseY = geo.scaleY(0);
				return geo.resolved.map((s, si) => {
					const color = chartSeriesColor(si);
					return (
						<g key={s.name}>
							{geo.labels.map((label, i) => {
								const value = s.values[i];
								if (value === null) return null;
								const groupStart = geo.xs[i] - group / 2;
								const x = groupStart + si * (barWidth + gap);
								return (
									<path
										key={label}
										d={barPath(x, barWidth, baseY, geo.scaleY(value), 4)}
										fill={color}
									/>
								);
							})}
						</g>
					);
				});
			}}
			overlay={(geo, index) => {
				const slot =
					(geo.plotRight - geo.plotLeft) / Math.max(1, geo.labels.length);
				return (
					<rect
						x={geo.xs[index] - slot / 2}
						y={geo.plotTop}
						width={slot}
						height={geo.plotBottom - geo.plotTop}
						className="fill-muted-foreground/10"
					/>
				);
			}}
		/>
	);
}
