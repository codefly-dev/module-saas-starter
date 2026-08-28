"use client";

import type * as React from "react";
import { useAuth } from "@/lib/auth";
import { Grid, Stack } from "@/shared/ui";
import type { DashboardDef, LayoutDef, MetricDef } from "../model/schema";
import { MetricCard } from "./metric-card";

// Column-span classes mirror Grid's responsive breakpoints so a spanning card
// widens in step with the grid. Callers clamp the span to the grid's column
// count before lookup: a span wider than the grid has tracks would make CSS
// grid spawn implicit columns and break the row, so it is never emitted.
const colSpan: Record<1 | 2 | 3 | 4, string> = {
	1: "",
	2: "sm:col-span-2",
	3: "sm:col-span-2 lg:col-span-3",
	4: "sm:col-span-2 lg:col-span-4",
};

// A metric's React key is derived from what makes it distinct — not its array
// index (reorder-fragile) nor its title alone (two panels can share a title).
function metricKey(metric: MetricDef): string {
	return [
		metric.title,
		metric.chart,
		Array.isArray(metric.groupBy) ? metric.groupBy.join(",") : metric.groupBy,
		metric.bucket ?? "",
		metric.event?.type ?? "",
		metric.category ?? "",
		metric.from ?? "",
		metric.to ?? "",
		metric.span ?? "",
		JSON.stringify(metric.value ?? metric.ratio ?? 0),
	].join("|");
}

// Dashboard renders a declared data graph. Every metric reads the audit trail
// scoped to the viewer's organization by default; pass `orgId` to pin it.
export function Dashboard({
	data,
	orgId,
}: {
	data: DashboardDef;
	orgId?: string;
}) {
	const { organizationId } = useAuth();
	const resolvedOrgId = orgId ?? organizationId ?? "";

	const layout: LayoutDef = data.layout ?? { kind: "grid" };
	const columns = layout.columns ?? 2;
	// Accent themes the dashboard's charts by overriding the primary token for
	// this subtree; the charts already color from `primary`, so nothing below
	// needs to know about the accent.
	const style = data.theme?.accent
		? ({ "--primary": data.theme.accent } as React.CSSProperties)
		: undefined;

	const cards = data.metrics.map((metric) => (
		<MetricCard
			key={metricKey(metric)}
			metric={metric}
			orgId={resolvedOrgId}
			className={
				layout.kind === "grid" && metric.span
					? colSpan[Math.min(metric.span, columns) as 1 | 2 | 3 | 4]
					: undefined
			}
		/>
	));

	return (
		<div className="space-y-4" style={style}>
			{(data.title || data.description) && (
				<div className="space-y-1">
					{data.title && (
						<h2 className="text-lg font-semibold tracking-tight">
							{data.title}
						</h2>
					)}
					{data.description && (
						<p className="text-sm text-muted-foreground">{data.description}</p>
					)}
				</div>
			)}
			{layout.kind === "stack" ? (
				<Stack gap={4}>{cards}</Stack>
			) : (
				<Grid cols={columns} gap={4}>
					{cards}
				</Grid>
			)}
		</div>
	);
}
