"use client";

// One resolved widget's values, drawn with the kit's metric charts and tiles:
// the same primitives a page that builds its own dashboard (the audit log, say)
// draws with, so a declared dashboard looks like the rest of the app. The kit's
// <Dashboard> and the host's solution page both draw a tile's values with this,
// and each says for itself what a tile with no values shows.

import { BarList } from "./charts.js";
import { formatShare } from "./format.js";
import { AreaChart, LineChart } from "./metric-chart.js";
import type { ChartSeries } from "./metric-geometry.js";
import { type Metric, MetricFigure } from "./metric-tiles.js";
import type { DashboardWidgetView } from "./types.js";

// A tile's number as the metric figure writes it. A percent metric is a share
// from 0 to 1, and the figure's `percent` expects it already scaled (45 →
// "45%"), so the share is scaled here, once.
function figureOf(widget: DashboardWidgetView, total: number): Metric {
	const percent = widget.format === "percent";
	return {
		label: widget.title ?? widget.id,
		value: percent ? total * 100 : total,
		format: percent ? "percent" : "number",
		series: widget.series.points.map((point) => point.value),
	};
}

/**
 * Draw a resolved widget's values. Expects values to draw: a caller checks for
 * an empty series (and a `number` widget with no total) first, since what such
 * a tile says differs between pages.
 */
export function WidgetChart({ widget }: { widget: DashboardWidgetView }) {
	const { series, visualization } = widget;
	// Undefined for a plain number, so each chart keeps its own default.
	const formatValue = widget.format === "percent" ? formatShare : undefined;
	const title = widget.title ?? widget.id;
	const chartSeries: ChartSeries[] = [
		{
			name: title,
			data: series.points.map((point) => ({
				label: point.key,
				value: point.value,
			})),
		},
	];
	switch (visualization) {
		case "line":
			return (
				<LineChart
					title={title}
					series={chartSeries}
					formatValue={formatValue}
				/>
			);
		case "area":
			return (
				<AreaChart
					title={title}
					series={chartSeries}
					formatValue={formatValue}
				/>
			);
		case "bar":
			return <BarList points={series.points} formatValue={formatValue} />;
		case "number":
			return series.total === null ? null : (
				<MetricFigure metric={figureOf(widget, series.total)} />
			);
		case "table":
			return (
				<div className="overflow-x-auto">
					<table className="w-full type-body">
						<tbody>
							{series.points.map((point) => (
								<tr key={point.key} className="border-b last:border-0">
									<td className="py-1 pr-4 text-muted-foreground">
										{point.key}
									</td>
									<td className="py-1 text-right tabular-nums">
										{formatValue
											? formatValue(point.value)
											: point.value.toLocaleString()}
									</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			);
		default: {
			// Compile-time exhaustiveness: a new WidgetVisualization must be
			// handled here or this assignment fails to type-check.
			const _exhaustive: never = visualization;
			return _exhaustive;
		}
	}
}
