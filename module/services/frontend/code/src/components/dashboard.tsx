/**
 * Dashboard — a data-driven grid renderer for the widget slot.
 *
 * A consumer describes a dashboard as data (`DashboardData`) and drops
 * `<Dashboard data={…} />` into a page. The renderer owns the header,
 * the responsive grid, and — first-class — the loading / error / empty
 * states of each widget, so a page never has to hand-roll them.
 *
 * It is deliberately source-agnostic: the audit feature is the first
 * consumer, but nothing here is audit-specific. New widget kinds slot
 * into the `DashboardWidget` union without touching consumers.
 */

import {
	BarList,
	type ChartSeries,
	KPIRow,
	type Metric,
	MetricAreaChart,
} from "@codefly-dev/ui/dashboard";
import {
	EmptyState,
	ErrorState,
	Page,
	PageHeader,
} from "@codefly-dev/ui/layout";
import type { ReactNode } from "react";
import { Sparkline } from "@/components/sparkline";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { readOutcome } from "@/shared/lib/read-outcome";

interface WidgetIdentity {
	id: string;
	/** `"full"` spans every grid column; omit for a single column. */
	span?: "full";
}

/** Data widgets the renderer wraps in a card and drives states for. */
interface CardWidgetBase extends WidgetIdentity {
	title?: string;
	description?: string;
	isLoading?: boolean;
	error?: unknown;
	/** Shown in place of content when the widget resolves to no data. */
	emptyMessage?: string;
}

/** A time series drawn as a sparkline. Empty when there is nothing to plot. */
export interface SparklineWidget extends CardWidgetBase {
	kind: "sparkline";
	points: number[];
}

/** A ranked list of labelled values drawn as proportional bars. */
export interface BarsWidget extends CardWidgetBase {
	kind: "bars";
	items: { label: string; value: number }[];
}

/**
 * An escape hatch for rich content (e.g. a table) that owns its own
 * chrome and states. Rendered bare — no card, no state framework — so
 * it carries only identity and layout, never the card state fields.
 */
export interface NodeWidget extends WidgetIdentity {
	kind: "node";
	node: ReactNode;
}

/**
 * A row of headline numbers with optional period deltas. Spans the grid: a
 * tile row is a summary of the page, not one card among peers.
 */
export interface MetricsWidget extends CardWidgetBase {
	kind: "metrics";
	metrics: Metric[];
}

/**
 * Several aligned series over one axis, drawn stacked. Each series carries
 * every label; the kit aligns by index, so a caller zero-fills gaps first.
 */
export interface SeriesWidget extends CardWidgetBase {
	kind: "series";
	series: ChartSeries[];
	/** Stack the series so the outline is the total. Defaults to true. */
	stacked?: boolean;
	formatValue?: (value: number) => string;
}

export type DashboardWidget =
	| SparklineWidget
	| BarsWidget
	| MetricsWidget
	| SeriesWidget
	| NodeWidget;

export interface DashboardData {
	title?: string;
	description?: string;
	/** Toolbar rendered on the right of the header (filters, exports…). */
	actions?: ReactNode;
	widgets: DashboardWidget[];
}

function spanClass(span?: "full") {
	return span === "full" ? "md:col-span-2" : undefined;
}

/** Retain useful data after transient failure, but never after access is denied. */
function withState(
	widget: CardWidgetBase,
	isEmpty: boolean,
	content: ReactNode,
): ReactNode {
	if (widget.error && readOutcome(true, widget.error) === "forbidden")
		return (
			<ErrorState
				title="Access is unavailable"
				detail="You don't have access to this information. Ask your administrator to grant access."
			/>
		);
	if (widget.isLoading && !widget.error)
		return (
			<div role="status" aria-label="Loading summary">
				<Skeleton className="h-16 w-full" />
			</div>
		);
	if (widget.error)
		return (
			<div className="space-y-3">
				<ErrorState
					title={
						isEmpty
							? "Couldn't load this summary"
							: "Couldn't refresh this summary"
					}
					detail={
						isEmpty
							? "The information is unavailable. Refresh the page to try again."
							: "The information below may be out of date. Refresh the page to try again."
					}
				/>
				{!isEmpty && content}
			</div>
		);
	if (!isEmpty) return content;
	return (
		<EmptyState
			heading={widget.emptyMessage ?? "No activity in this period"}
			description="Choose a different time range or adjust the filters to look for activity."
		/>
	);
}

function CardWidget({
	widget,
	isEmpty,
	children,
}: {
	widget: CardWidgetBase;
	isEmpty: boolean;
	children: ReactNode;
}) {
	return (
		<Card className={spanClass(widget.span)}>
			{(widget.title || widget.description) && (
				<CardHeader>
					{widget.title && (
						<CardTitle role="heading" aria-level={2} className="text-base">
							{widget.title}
						</CardTitle>
					)}
					{widget.description && (
						<CardDescription>{widget.description}</CardDescription>
					)}
				</CardHeader>
			)}
			<CardContent>{withState(widget, isEmpty, children)}</CardContent>
		</Card>
	);
}

function Widget({ widget }: { widget: DashboardWidget }) {
	switch (widget.kind) {
		case "sparkline":
			return (
				<CardWidget widget={widget} isEmpty={widget.points.length === 0}>
					<Sparkline points={widget.points} className="text-primary/70" />
				</CardWidget>
			);
		case "bars":
			return (
				<CardWidget widget={widget} isEmpty={widget.items.length === 0}>
					<BarList
						points={widget.items.map(({ label, value }) => ({
							key: label,
							value,
						}))}
					/>
				</CardWidget>
			);
		case "metrics":
			// A tile row is chrome-free: the tiles are the cards. The state
			// framework still applies while there is nothing to show.
			return (
				<div className={spanClass("full")}>
					{withState(
						widget,
						widget.metrics.length === 0,
						<KPIRow metrics={widget.metrics} />,
					)}
				</div>
			);
		case "series":
			return (
				<CardWidget
					widget={widget}
					isEmpty={widget.series.every((s) => s.data.length === 0)}
				>
					<MetricAreaChart
						title={widget.title ?? "Series"}
						series={widget.series}
						stacked={widget.stacked ?? true}
						formatValue={widget.formatValue}
					/>
				</CardWidget>
			);
		case "node":
			return <div className={spanClass(widget.span)}>{widget.node}</div>;
		default: {
			// Compile-time exhaustiveness: a new widget kind must be handled
			// here or this assignment fails to type-check.
			const _exhaustive: never = widget;
			return _exhaustive;
		}
	}
}

export function Dashboard({ data }: { data: DashboardData }) {
	const hasHeader = data.title || data.description || data.actions;
	return (
		<Page>
			{hasHeader && (
				<PageHeader title={data.title} description={data.description}>
					{data.actions && (
						<div className="flex flex-wrap items-center gap-2 pt-3">
							{data.actions}
						</div>
					)}
				</PageHeader>
			)}

			<div className="grid gap-4 md:grid-cols-2">
				{data.widgets.map((widget) => (
					<Widget key={widget.id} widget={widget} />
				))}
			</div>
		</Page>
	);
}
