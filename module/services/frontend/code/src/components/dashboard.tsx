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

export type DashboardWidget = SparklineWidget | BarsWidget | NodeWidget;

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

/**
 * Retained content wins over transient states: as long as the widget has
 * data to render, a background refetch or a refetch *error* never blanks
 * it — the last good content stays on screen. Only when there is nothing
 * to show do we surface error → loading → empty, in that order.
 */
function withState(
	widget: CardWidgetBase,
	isEmpty: boolean,
	content: ReactNode,
): ReactNode {
	if (!isEmpty) {
		return content;
	}
	if (widget.error) {
		return <p className="text-sm text-destructive">Failed to load.</p>;
	}
	if (widget.isLoading) {
		return <Skeleton className="h-16 w-full" />;
	}
	return (
		<p className="text-sm text-muted-foreground">
			{widget.emptyMessage ?? "No data."}
		</p>
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
						<CardTitle className="text-base">{widget.title}</CardTitle>
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

function Bars({ items }: { items: BarsWidget["items"] }) {
	const max = items.reduce((m, item) => Math.max(m, item.value), 0) || 1;
	return (
		<div className="space-y-2">
			{items.map((item, i) => (
				<div key={`${i}-${item.label}`} className="space-y-1">
					<div className="flex items-center justify-between text-xs">
						<span className="text-muted-foreground">{item.label}</span>
						<span className="font-mono">{item.value}</span>
					</div>
					<div className="h-2 rounded-full bg-muted">
						<div
							className="h-2 rounded-full bg-primary/70"
							style={{ width: `${(item.value / max) * 100}%` }}
						/>
					</div>
				</div>
			))}
		</div>
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
					<Bars items={widget.items} />
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
		<div className="space-y-6">
			{hasHeader && (
				<div className="flex items-center justify-between">
					<div>
						{data.title && (
							<h1 className="text-2xl font-bold tracking-tight">{data.title}</h1>
						)}
						{data.description && (
							<p className="text-muted-foreground">{data.description}</p>
						)}
					</div>
					{data.actions && (
						<div className="flex items-center gap-2">{data.actions}</div>
					)}
				</div>
			)}

			<div className="grid gap-4 md:grid-cols-2">
				{data.widgets.map((widget) => (
					<Widget key={widget.id} widget={widget} />
				))}
			</div>
		</div>
	);
}
