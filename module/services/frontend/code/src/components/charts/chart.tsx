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
 * Accessibility: the SVG is decorative (`aria-hidden`); identity never rests on
 * colour alone (a legend keys every series for two-or-more, tabular-nums), and
 * the exact numbers live in a visually-hidden data table that is the chart's
 * screen-reader representation.
 */

"use client";

import { type ReactNode, useState } from "react";
import { cn } from "@/lib/utils";
import {
	areaPath,
	axisPositions,
	type ChartSeries,
	linearScale,
	linePath,
	niceTicks,
	valueExtent,
} from "./geometry";

const VIEW_W = 640;
const DEFAULT_VIEW_H = 260;
const MARGIN = { top: 12, right: 16, bottom: 28, left: 48 };
const MAX_X_LABELS = 8;
const PALETTE_SIZE = 5;

const compactFormat = new Intl.NumberFormat("en-US", {
	notation: "compact",
	maximumFractionDigits: 1,
});
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
	/** Names the chart for the accessible label; also shown by consumers' card. */
	title: string;
	className?: string;
	/** viewBox height; width is fluid. Defaults to 260. */
	height?: number;
	/** Formats values in the tooltip and table. Defaults to a grouped integer. */
	formatValue?: (value: number) => string;
}

interface FrameGeometry {
	viewH: number;
	plotLeft: number;
	plotRight: number;
	plotTop: number;
	plotBottom: number;
	labels: string[];
	ticks: number[];
	scaleY: (value: number) => number;
}

function useFrameGeometry(
	series: ChartSeries[],
	height: number,
): FrameGeometry {
	const viewH = height;
	const plotLeft = MARGIN.left;
	const plotRight = VIEW_W - MARGIN.right;
	const plotTop = MARGIN.top;
	const plotBottom = viewH - MARGIN.bottom;
	const labels = series[0]?.data.map((d) => d.label) ?? [];
	const [min, max] = valueExtent(series);
	const ticks = niceTicks(min, max);
	const domainMin = ticks[0];
	const domainMax = ticks[ticks.length - 1];
	const scaleY = linearScale(domainMin, domainMax, plotBottom, plotTop);
	return {
		viewH,
		plotLeft,
		plotRight,
		plotTop,
		plotBottom,
		labels,
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
	shown.add(count - 1);
	return shown;
}

function Legend({
	series,
	marker,
}: {
	series: ChartSeries[];
	marker: "line" | "rect";
}) {
	if (series.length < 2) return null;
	return (
		<ul className="flex flex-wrap gap-x-4 gap-y-1">
			{series.map((s, i) => (
				<li
					key={s.name}
					className="flex items-center gap-1.5 text-xs text-muted-foreground"
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
	series,
	labels,
	title,
	formatValue,
}: {
	series: ChartSeries[];
	labels: string[];
	title: string;
	formatValue: (value: number) => string;
}) {
	return (
		<table className="sr-only">
			<caption>{title}</caption>
			<thead>
				<tr>
					<th scope="col">Bucket</th>
					{series.map((s) => (
						<th key={s.name} scope="col">
							{s.name}
						</th>
					))}
				</tr>
			</thead>
			<tbody>
				{labels.map((label, row) => (
					<tr key={label}>
						<th scope="row">{label}</th>
						{series.map((s) => (
							<td key={s.name}>{formatValue(s.data[row]?.value ?? 0)}</td>
						))}
					</tr>
				))}
			</tbody>
		</table>
	);
}

function Axes({ geo }: { geo: FrameGeometry }) {
	const shown = xTickIndices(geo.labels.length);
	const xs = axisPositions(geo.labels.length, geo.plotLeft, geo.plotRight);
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
							className="fill-muted-foreground text-[10px] tabular-nums"
						>
							{compactFormat.format(tick)}
						</text>
					</g>
				);
			})}
			{geo.labels.map((label, i) =>
				shown.has(i) ? (
					<text
						key={label}
						x={xs[i]}
						y={geo.plotBottom + 16}
						textAnchor="middle"
						className="fill-muted-foreground text-[10px]"
					>
						{label}
					</text>
				) : null,
			)}
		</>
	);
}

interface HoverState {
	index: number | null;
	set: (index: number | null) => void;
}

/** Snaps the pointer to the nearest column and reports its index. */
function HoverOverlay({
	geo,
	xs,
	hover,
}: {
	geo: FrameGeometry;
	xs: number[];
	hover: HoverState;
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
				for (let i = 0; i < xs.length; i++) {
					const distance = Math.abs(xs[i] - viewX);
					if (distance < bestDistance) {
						bestDistance = distance;
						best = i;
					}
				}
				hover.set(best);
			}}
			onPointerLeave={() => hover.set(null)}
		/>
	);
}

function Tooltip({
	xs,
	index,
	series,
	labels,
	formatValue,
}: {
	xs: number[];
	index: number;
	series: ChartSeries[];
	labels: string[];
	formatValue: (value: number) => string;
}) {
	const leftPercent = (xs[index] / VIEW_W) * 100;
	return (
		<div
			className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 rounded-md border bg-popover px-2.5 py-1.5 text-popover-foreground shadow-md"
			style={{ left: `${leftPercent}%` }}
			aria-hidden
		>
			<div className="mb-1 text-[11px] text-muted-foreground">
				{labels[index]}
			</div>
			<ul className="space-y-0.5">
				{series.map((s, i) => (
					<li key={s.name} className="flex items-center gap-2 text-xs">
						<span
							className="inline-block h-0.5 w-3 rounded-full"
							style={{ backgroundColor: chartSeriesColor(i) }}
							aria-hidden
						/>
						<span className="text-muted-foreground">{s.name}</span>
						<span className="ml-auto font-medium tabular-nums">
							{formatValue(s.data[index]?.value ?? 0)}
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
			<div className="flex h-full items-center justify-center text-sm text-muted-foreground">
				No data
			</div>
		</figure>
	);
}

/**
 * Shared frame for the cartesian charts. `marks` draws the series geometry;
 * `overlay` draws the hover response (crosshair, markers, band highlight) for
 * the active column.
 */
function ChartFrame({
	series,
	title,
	className,
	height = DEFAULT_VIEW_H,
	formatValue = (value) => fullFormat.format(value),
	legendMarker,
	marks,
	overlay,
	bandHover,
}: MetricChartProps & {
	legendMarker: "line" | "rect";
	marks: (geo: FrameGeometry, xs: number[]) => ReactNode;
	overlay: (geo: FrameGeometry, xs: number[], index: number) => ReactNode;
	bandHover?: boolean;
}) {
	const [active, setActive] = useState<number | null>(null);
	const geo = useFrameGeometry(series, height);

	if (series.length === 0 || geo.labels.length === 0) {
		return <EmptyChart title={title} height={height} className={className} />;
	}

	const xs = axisPositions(
		geo.labels.length,
		geo.plotLeft,
		geo.plotRight,
		bandHover,
	);

	return (
		<figure className={cn("relative w-full", className)}>
			{series.length >= 2 && (
				<figcaption className="mb-2">
					<Legend series={series} marker={legendMarker} />
				</figcaption>
			)}
			<svg
				viewBox={`0 0 ${VIEW_W} ${height}`}
				className="w-full"
				style={{ aspectRatio: `${VIEW_W} / ${height}` }}
				preserveAspectRatio="none"
				aria-hidden
			>
				<Axes geo={geo} />
				{marks(geo, xs)}
				{active !== null && overlay(geo, xs, active)}
				<HoverOverlay
					geo={geo}
					xs={xs}
					hover={{ index: active, set: setActive }}
				/>
			</svg>
			{active !== null && (
				<Tooltip
					xs={xs}
					index={active}
					series={series}
					labels={geo.labels}
					formatValue={formatValue}
				/>
			)}
			<DataTable
				series={series}
				labels={geo.labels}
				title={title}
				formatValue={formatValue}
			/>
		</figure>
	);
}

function seriesPoints(
	series: ChartSeries,
	xs: number[],
	scaleY: (value: number) => number,
) {
	return series.data.map((d, i) => ({ x: xs[i], y: scaleY(d.value) }));
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

export function LineChart(props: MetricChartProps) {
	return (
		<ChartFrame
			{...props}
			legendMarker="line"
			marks={(geo, xs) =>
				props.series.map((s, i) => {
					const points = seriesPoints(s, xs, geo.scaleY);
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
							{last && (
								<circle
									cx={last.x}
									cy={last.y}
									r={3.5}
									fill={color}
									className="stroke-card"
									strokeWidth={2}
									vectorEffect="non-scaling-stroke"
								/>
							)}
						</g>
					);
				})
			}
			overlay={(geo, xs, index) => (
				<g>
					<Crosshair geo={geo} x={xs[index]} />
					{props.series.map((s, i) => (
						<circle
							key={s.name}
							cx={xs[index]}
							cy={geo.scaleY(s.data[index]?.value ?? 0)}
							r={3.5}
							fill={chartSeriesColor(i)}
							className="stroke-card"
							strokeWidth={2}
							vectorEffect="non-scaling-stroke"
						/>
					))}
				</g>
			)}
		/>
	);
}

export function AreaChart(props: MetricChartProps) {
	return (
		<ChartFrame
			{...props}
			legendMarker="line"
			marks={(geo, xs) =>
				props.series.map((s, i) => {
					const points = seriesPoints(s, xs, geo.scaleY);
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
							{last && (
								<circle
									cx={last.x}
									cy={last.y}
									r={3.5}
									fill={color}
									className="stroke-card"
									strokeWidth={2}
									vectorEffect="non-scaling-stroke"
								/>
							)}
						</g>
					);
				})
			}
			overlay={(geo, xs, index) => (
				<g>
					<Crosshair geo={geo} x={xs[index]} />
					{props.series.map((s, i) => (
						<circle
							key={s.name}
							cx={xs[index]}
							cy={geo.scaleY(s.data[index]?.value ?? 0)}
							r={3.5}
							fill={chartSeriesColor(i)}
							className="stroke-card"
							strokeWidth={2}
							vectorEffect="non-scaling-stroke"
						/>
					))}
				</g>
			)}
		/>
	);
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
	const seriesCount = props.series.length;
	return (
		<ChartFrame
			{...props}
			legendMarker="rect"
			bandHover
			marks={(geo, xs) => {
				const bandCount = geo.labels.length;
				const slot = (geo.plotRight - geo.plotLeft) / Math.max(1, bandCount);
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
				return props.series.map((s, si) => {
					const color = chartSeriesColor(si);
					return (
						<g key={s.name}>
							{s.data.map((d, i) => {
								const groupStart = xs[i] - group / 2;
								const x = groupStart + si * (barWidth + gap);
								const valueY = geo.scaleY(d.value);
								return (
									<path
										key={d.label}
										d={barPath(x, barWidth, baseY, valueY, 4)}
										fill={color}
									/>
								);
							})}
						</g>
					);
				});
			}}
			overlay={(geo, xs, index) => {
				const bandCount = geo.labels.length;
				const slot = (geo.plotRight - geo.plotLeft) / Math.max(1, bandCount);
				return (
					<rect
						x={xs[index] - slot / 2}
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
