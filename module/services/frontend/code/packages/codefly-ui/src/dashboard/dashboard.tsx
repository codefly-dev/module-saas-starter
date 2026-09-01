"use client";

// The shared, pure <Dashboard> renderer. It takes a fully-resolved DashboardView
// — widgets already bound to their series — and paints it. It fetches nothing,
// reads no host context, and imports no app code, so the host app and a
// solution's Module-Federation remote render identical dashboards from the same
// package instance. Data resolution (metric → audit query) is the job of
// `@codefly/saas-sdk`'s `runDashboard`; use `fromDashboardData` to bridge.

import type * as React from "react";
import { Card, Section } from "../layout/card.js";
import { AreaChart, BarList, LineChart, StatChart } from "./charts.js";
import { cn } from "./cn.js";
import type { DashboardView, DashboardWidgetView } from "./types.js";

// Column-span utility classes, matching the responsive grid below so a spanning
// card widens in step with it. A span is clamped to the grid's column count by
// the caller before lookup.
const COL_SPAN: Record<1 | 2 | 3 | 4, string> = {
	1: "",
	2: "sm:col-span-2",
	3: "sm:col-span-2 lg:col-span-3",
	4: "sm:col-span-2 lg:col-span-4",
};

const GRID_COLS: Record<1 | 2 | 3 | 4, string> = {
	1: "grid-cols-1",
	2: "grid-cols-1 sm:grid-cols-2",
	3: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-3",
	4: "grid-cols-1 sm:grid-cols-2 lg:grid-cols-4",
};

function WidgetBody({ widget }: { widget: DashboardWidgetView }) {
	const { series, visualization } = widget;
	if (series.points.length === 0) {
		return <div className="py-6 text-sm text-muted-foreground">No data yet.</div>;
	}
	switch (visualization) {
		case "line":
			return <LineChart points={series.points} className="text-primary" />;
		case "area":
			return <AreaChart points={series.points} className="text-primary" />;
		case "bar":
			return <BarList points={series.points} />;
		case "number":
			return <StatChart total={series.total} points={series.points} />;
		case "table":
			return (
				<div className="overflow-x-auto">
					<table className="w-full text-sm">
						<tbody>
							{series.points.map((p) => (
								<tr key={p.key} className="border-b last:border-0">
									<td className="py-1 pr-4 text-muted-foreground">{p.key}</td>
									<td className="py-1 text-right tabular-nums">{p.value.toLocaleString()}</td>
								</tr>
							))}
						</tbody>
					</table>
				</div>
			);
		default:
			return null;
	}
}

function WidgetCard({ widget, columns }: { widget: DashboardWidgetView; columns: 1 | 2 | 3 | 4 }) {
	const span = widget.span ? (Math.min(widget.span, columns) as 1 | 2 | 3 | 4) : 1;
	return (
		<Card title={widget.title} className={COL_SPAN[span]}>
			<WidgetBody widget={widget} />
		</Card>
	);
}

/**
 * Render a resolved dashboard. Pass a `DashboardView` (from your own data or via
 * `fromDashboardData(runDashboard(...))`). `accent` overrides the `--primary`
 * token for this dashboard's subtree only, so every chart picks it up.
 */
export function Dashboard({ data, className }: { data: DashboardView; className?: string }) {
	const columns = data.columns ?? 2;
	const style = data.accent ? ({ "--primary": data.accent } as React.CSSProperties) : undefined;
	const isGrid = (data.layout ?? "grid") === "grid";

	return (
		<Section title={data.title} description={data.description} className={className} style={style}>
			<div className={isGrid ? cn("grid gap-4", GRID_COLS[columns]) : "flex flex-col gap-4"}>
				{data.widgets.map((widget) => (
					<WidgetCard key={widget.id} widget={widget} columns={columns} />
				))}
			</div>
		</Section>
	);
}
