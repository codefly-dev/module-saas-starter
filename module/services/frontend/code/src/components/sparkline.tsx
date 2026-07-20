/**
 * Sparkline — tiny inline-SVG line chart for at-a-glance trend
 * indicators (audit volume, MAU, MRR, etc).
 *
 * Why pure SVG and not Recharts: a sparkline is one path. Adding a
 * 50KB charting lib for one path on the dashboard is a poor tradeoff.
 * Pure SVG also means zero hydration cost and trivial server-side
 * rendering.
 *
 * Design choices:
 *   - Defaults sized for a card-corner accent (96×24px); override via
 *     width/height props.
 *   - Stroke uses currentColor so consumers control the hue via
 *     Tailwind text classes (`text-amber-500`).
 *   - A subtle gradient fill below the line gives depth without
 *     screaming "chart" — the line stays the focal point.
 *   - When all points are equal we draw a flat midline rather than
 *     dividing by zero; the visual "no change" answer is correct.
 */

import { useId } from "react";

interface SparklineProps {
	points: number[];
	width?: number;
	height?: number;
	className?: string;
	// strokeWidth defaults to 1.5 — looks crisp at 1× and 2× DPR.
	strokeWidth?: number;
}

export function Sparkline({
	points,
	width = 96,
	height = 24,
	strokeWidth = 1.5,
	className,
}: SparklineProps) {
	const gradId = `sparkline-grad-${useId().replaceAll(":", "")}`;

	if (points.length < 2) {
		return (
			<svg
				width={width}
				height={height}
				viewBox={`0 0 ${width} ${height}`}
				className={className}
				aria-hidden
			/>
		);
	}

	const min = Math.min(...points);
	const max = Math.max(...points);
	const range = max - min || 1;
	const stepX = width / (points.length - 1);

	const coords = points.map((v, i) => {
		const x = i * stepX;
		// Invert Y so larger values are higher on screen.
		const y =
			height - ((v - min) / range) * (height - strokeWidth) - strokeWidth / 2;
		return [x, y] as const;
	});

	const linePath = coords
		.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`)
		.join(" ");

	// Closed area under the line for the gradient fill.
	const areaPath = `${linePath} L${width.toFixed(2)},${height.toFixed(2)} L0,${height.toFixed(2)} Z`;

	return (
		<svg
			width={width}
			height={height}
			viewBox={`0 0 ${width} ${height}`}
			className={className}
			aria-hidden
		>
			<defs>
				<linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
					<stop offset="0%" stopColor="currentColor" stopOpacity="0.25" />
					<stop offset="100%" stopColor="currentColor" stopOpacity="0" />
				</linearGradient>
			</defs>
			<path d={areaPath} fill={`url(#${gradId})`} />
			<path
				d={linePath}
				fill="none"
				stroke="currentColor"
				strokeWidth={strokeWidth}
				strokeLinecap="round"
				strokeLinejoin="round"
			/>
		</svg>
	);
}
