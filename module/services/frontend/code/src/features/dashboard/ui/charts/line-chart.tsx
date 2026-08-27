import { useId } from "react";

// LineChart — a card-sized pure-SVG area+line chart for a time series. Same
// rationale as the dashboard Sparkline (no charting lib for a single path),
// scaled up: it stretches to its container via a fixed viewBox with
// non-uniform scaling, and colors from currentColor so callers pick the hue
// with a Tailwind text class.
interface LineChartProps {
	points: number[];
	className?: string;
	height?: number;
}

const VIEW_W = 600;

export function LineChart({ points, className, height = 160 }: LineChartProps) {
	const gradId = `line-chart-grad-${useId().replaceAll(":", "")}`;

	if (points.length === 0) return null;

	const min = Math.min(...points);
	const max = Math.max(...points);
	const range = max - min || 1;
	const pad = 4;

	const coords = points.map((v, i) => {
		const x =
			points.length === 1 ? VIEW_W / 2 : (i * VIEW_W) / (points.length - 1);
		const y = height - pad - ((v - min) / range) * (height - pad * 2);
		return [x, y] as const;
	});

	// A single bucket has no slope to draw and no scale to place it on; extend it
	// into a flat baseline at mid-height across the width so the card shows the
	// datum exists instead of an empty box (or a bottom-pinned line reading zero).
	const anchors: ReadonlyArray<readonly [number, number]> =
		coords.length === 1
			? [
					[0, height / 2],
					[VIEW_W, height / 2],
				]
			: coords;

	const line = anchors
		.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`)
		.join(" ");
	const area = `${line} L${VIEW_W},${height} L0,${height} Z`;

	return (
		<svg
			viewBox={`0 0 ${VIEW_W} ${height}`}
			preserveAspectRatio="none"
			className={className}
			style={{ width: "100%", height }}
			role="img"
			aria-label="Line chart"
		>
			<defs>
				<linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
					<stop offset="0%" stopColor="currentColor" stopOpacity="0.2" />
					<stop offset="100%" stopColor="currentColor" stopOpacity="0" />
				</linearGradient>
			</defs>
			<path d={area} fill={`url(#${gradId})`} />
			<path
				d={line}
				fill="none"
				stroke="currentColor"
				strokeWidth={2}
				vectorEffect="non-scaling-stroke"
				strokeLinecap="round"
				strokeLinejoin="round"
			/>
		</svg>
	);
}
