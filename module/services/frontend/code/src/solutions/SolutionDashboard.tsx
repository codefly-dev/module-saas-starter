"use client";

import type {
	Dashboard,
	DataGraph,
	MetricWidget,
} from "@codefly/saas-plugin-manifest";
import {
	createSaasClient,
	type ResolvedWidget,
	runDashboard,
} from "@codefly-dev/saas-sdk";
// Charts come from the shared kit, not host-internal components: the same
// primitives a solution's own remote would render with, so host-rendered and
// solution-rendered dashboards look identical and there is one charting
// implementation to maintain.
import {
	AreaChart,
	BarList,
	LineChart,
	StatChart,
} from "@codefly-dev/ui/dashboard";
import { useQuery } from "@tanstack/react-query";
import { GripVertical, Plus, X } from "lucide-react";
import {
	type DragEvent,
	type ReactNode,
	useState,
	useSyncExternalStore,
} from "react";
import { flushSync } from "react-dom";
import { scopedDashboardDraftKey } from "@/features/dashboard/service/use-dashboard-authoring";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import { cn } from "@/shared/lib/utils";
import {
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	Skeleton,
} from "@/shared/ui";
import {
	addableTiles,
	addTile,
	dropOn,
	layoutKey,
	moveBy,
	parseLayout,
	readSavedLayout,
	removeTile,
	serializeLayout,
	tileWidget,
	writeSavedLayout,
} from "./dashboard-layout";

// One audit-backed SDK client for every solution dashboard, built on the host's
// shared Connect transport so a solution's declared graph resolves through the
// same auth'd, org-scoped gateway path the rest of the app uses. The host owns
// the client, the bearer token, and the org scope; the solution supplies only
// the DataGraph — it can never widen a query past the viewer's own org.
const sdk = createSaasClient(apiTransport);

// Dashboard id of the throwaway single-widget graph each card resolves. The card
// resolves its own widget in isolation, so this id is never surfaced.
const SINGLE_WIDGET_DASHBOARD = "widget";

// A graph carrying every event and metric but exactly one widget, so runDashboard
// resolves only that widget's metric (and its derived inputs). Resolving each
// widget through its own call is what isolates failures: a widget whose metric's
// audit query errors fails alone, instead of blanking every sibling that shares
// the dashboard's single batched resolution.
function singleWidgetGraph(graph: DataGraph, widget: MetricWidget): DataGraph {
	return {
		events: graph.events,
		metrics: graph.metrics,
		dashboards: [
			{ id: SINGLE_WIDGET_DASHBOARD, layout: "grid", widgets: [widget] },
		],
	};
}

// Render a resolved widget's series in the shape its visualization asks for. A
// series with no points is "no data yet" for every kind — the audit RPC omits a
// bucket rather than emitting a zero, so an empty series means nothing matched,
// not a real zero worth plotting.
function WidgetBody({ widget }: { widget: ResolvedWidget }) {
	return (
		<>
			{widget.series.coverage === "partial" && (
				<p className="text-sm text-muted-foreground" role="status">
					Partial telemetry
				</p>
			)}
			<WidgetValues widget={widget} />
		</>
	);
}

function WidgetValues({ widget }: { widget: ResolvedWidget }) {
	const { series, visualization } = widget;
	if (series.points.length === 0) {
		return (
			<p className="text-sm text-muted-foreground">
				{series.coverage === "partial"
					? "Telemetry unavailable or incomplete."
					: "No data yet."}
			</p>
		);
	}
	switch (visualization) {
		case "line":
			return (
				<LineChart points={series.points} className="text-primary/70" axes />
			);
		case "area":
			return (
				<AreaChart points={series.points} className="text-primary/70" axes />
			);
		case "bar":
			return <BarList points={series.points} />;
		case "number":
			return series.total === null ? (
				<p className="text-sm text-muted-foreground">
					{series.coverage === "partial"
						? "Incomplete telemetry; total unavailable."
						: "Total unavailable across groups."}
				</p>
			) : (
				<StatChart total={series.total} points={series.points} />
			);
		case "table":
			return (
				<table className="w-full text-sm">
					<tbody>
						{series.points.map((point) => (
							<tr key={point.key} className="border-b last:border-0">
								<td className="py-1 text-muted-foreground">{point.key}</td>
								<td className="py-1 text-right font-mono">
									{point.value.toLocaleString()}
								</td>
							</tr>
						))}
					</tbody>
				</table>
			);
		default: {
			// Compile-time exhaustiveness: a new WidgetVisualization must be
			// handled here or this assignment fails to type-check.
			const _exhaustive: never = visualization;
			return _exhaustive;
		}
	}
}

function WidgetCard({
	graph,
	widget,
	solutionId,
	dashboardId,
	orgId,
	grip,
	remove,
}: {
	graph: DataGraph;
	widget: MetricWidget;
	solutionId: string;
	dashboardId: string;
	orgId: string;
	grip: ReactNode;
	remove: ReactNode;
}) {
	// The graph is part of the key so a solution that redeploys with a changed
	// declaration refetches instead of serving another graph's cached series that
	// happens to share the same solution/dashboard/widget/org ids. Disabled until
	// the org resolves so the pre-org window reads as loading, never as empty.
	const { data, isPending, isError } = useQuery({
		queryKey: [
			"solution-widget",
			solutionId,
			dashboardId,
			widget.id,
			orgId,
			graph,
		],
		queryFn: () =>
			runDashboard(
				sdk.audit,
				singleWidgetGraph(graph, widget),
				SINGLE_WIDGET_DASHBOARD,
				{ orgId },
			).then((resolved) => resolved.widgets[0]),
		enabled: orgId !== "",
	});

	return (
		<Card>
			<CardHeader className="flex flex-row items-center gap-1 pb-2">
				{grip}
				<CardTitle className="min-w-0 flex-1 text-base">
					{widget.title ?? widget.metric}
				</CardTitle>
				{remove}
			</CardHeader>
			<CardContent>
				{isError ? (
					<p className="text-sm text-destructive">Unable to load.</p>
				) : isPending || !data ? (
					<Skeleton className="h-24 w-full" />
				) : (
					<WidgetBody widget={data} />
				)}
			</CardContent>
		</Card>
	);
}

// Reading `window.localStorage` can itself throw: blocked site data or a
// sandboxed frame raise a SecurityError from the getter, before any getItem. No
// storage means every viewer sees the declared default.
function browserStorage(): Storage | null {
	if (typeof window === "undefined") return null;
	try {
		return window.localStorage;
	} catch {
		return null;
	}
}

// The saved layout is read from storage on each render, so there is nothing to
// subscribe to; a change made on this page is held in state instead.
function noSubscription() {
	return () => {};
}

/**
 * One viewer's layout of one dashboard, kept in this browser. The server
 * cannot know what the browser saved, so it renders the declared default and
 * React swaps in the saved layout after hydration; `useSyncExternalStore` is
 * what makes that a defined re-render rather than a hydration mismatch.
 *
 * The layout is saved only on the viewer's own change, so a viewer who never
 * customizes keeps following the declared default. Browser storage is the
 * interim home: ADR 0007 names a per-user preferences field in the user
 * settings as the durable one, which does not exist yet.
 */
function useDashboardLayout(
	storageKey: string,
	graph: DataGraph,
	dashboard: Dashboard,
): [string[], (next: readonly string[]) => void] {
	const stored = useSyncExternalStore(
		noSubscription,
		() => readSavedLayout(browserStorage(), storageKey),
		() => null,
	);
	// The viewer's latest change on this page. It holds even when storage
	// refuses the write; it just will not outlive a reload.
	const [edited, setEdited] = useState<{ key: string; raw: string } | null>(
		null,
	);
	const layout = parseLayout(
		edited?.key === storageKey ? edited.raw : stored,
		graph,
		dashboard,
	);
	const save = (next: readonly string[]) => {
		const raw = serializeLayout(next, dashboard);
		setEdited({ key: storageKey, raw });
		writeSavedLayout(browserStorage(), storageKey, raw);
	};
	return [layout, save];
}

const ARROW_STEP: Partial<Record<string, number>> = {
	ArrowUp: -1,
	ArrowLeft: -1,
	ArrowDown: 1,
	ArrowRight: 1,
};

// Lists what the viewer can put on the dashboard: the declared widgets they
// removed, and the graph's metrics this dashboard has no widget for.
function AddTileMenu({
	label,
	addable,
	onAdd,
}: {
	label: string;
	addable: { id: string; label: string }[];
	onAdd: (tileId: string) => void;
}) {
	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				render={
					<Button
						type="button"
						variant="outline"
						size="icon-sm"
						className="ml-auto"
						aria-label={label}
					/>
				}
			>
				<Plus />
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" className="w-64">
				{addable.length === 0 ? (
					<DropdownMenuItem disabled>
						Every metric is on this dashboard
					</DropdownMenuItem>
				) : (
					addable.map((tile) => (
						<DropdownMenuItem key={tile.id} onClick={() => onAdd(tile.id)}>
							{tile.label}
						</DropdownMenuItem>
					))
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

// A dashboard the viewer can arrange: drag a tile to reorder (or focus its grip
// and use the arrow keys), × to remove one, + to add one back.
function SolutionDashboard({
	graph,
	dashboard,
	solutionId,
}: {
	graph: DataGraph;
	dashboard: Dashboard;
	solutionId: string;
}) {
	const { user, organizationId } = useAuth();
	const orgId = organizationId ?? "";
	const storageKey = scopedDashboardDraftKey(
		layoutKey(solutionId, dashboard.id),
		{ organizationId, userId: user?.id },
	);
	const [layout, save] = useDashboardLayout(storageKey, graph, dashboard);
	// While a tile is dragged the others reflow around it in `order`. The order
	// is saved on a drop and discarded when the drag is cancelled. `over` is the
	// tile last entered, so hovering one tile moves the dragged tile once rather
	// than on every dragover event.
	const [drag, setDrag] = useState<{
		tileId: string;
		over: string;
		order: string[];
	} | null>(null);

	// A drop anywhere on the dashboard, the gaps between tiles included, keeps
	// the new order.
	const onDragOver = (event: DragEvent) => {
		if (!drag) return;
		event.preventDefault();
		event.dataTransfer.dropEffect = "move";
	};
	const onDrop = (event: DragEvent) => {
		if (!drag) return;
		event.preventDefault();
		save(drag.order);
		setDrag(null);
	};

	const tiles = (drag?.order ?? layout).flatMap((tileId) => {
		const widget = tileWidget(graph, dashboard, tileId);
		if (!widget) return [];
		const title = widget.title ?? widget.metric;
		return [
			<li
				key={tileId}
				draggable
				className={cn("cursor-grab", drag?.tileId === tileId && "opacity-50")}
				onDragStart={(event) => {
					event.dataTransfer.effectAllowed = "move";
					// Firefox starts a drag only when it carries data.
					event.dataTransfer.setData("text/plain", tileId);
					setDrag({ tileId, over: tileId, order: layout });
				}}
				onDragOver={() => {
					// Entering another tile takes its place; the rest shift over.
					if (!drag || drag.over === tileId) return;
					setDrag({
						...drag,
						over: tileId,
						order: dropOn(drag.order, drag.tileId, tileId),
					});
				}}
				// Fires after a drop too, which has already saved.
				onDragEnd={() => setDrag(null)}
			>
				<WidgetCard
					graph={graph}
					widget={widget}
					solutionId={solutionId}
					dashboardId={dashboard.id}
					orgId={orgId}
					grip={
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							className="cursor-grab text-muted-foreground"
							aria-label={`Move ${title}: drag the tile, or use the arrow keys`}
							onKeyDown={(event) => {
								const step = ARROW_STEP[event.key];
								if (step === undefined) return;
								event.preventDefault();
								const grip = event.currentTarget;
								// Moving a tile can move its DOM node, which drops focus;
								// commit first so the grip can take focus back.
								flushSync(() => save(moveBy(layout, tileId, step)));
								grip.focus();
							}}
						>
							<GripVertical />
						</Button>
					}
					remove={
						<Button
							type="button"
							variant="ghost"
							size="icon-xs"
							className="text-muted-foreground"
							aria-label={`Remove ${title}`}
							onClick={() => save(removeTile(layout, tileId))}
						>
							<X />
						</Button>
					}
				/>
			</li>,
		];
	});

	return (
		<section className="space-y-4">
			<div className="flex items-center gap-2">
				{dashboard.title && (
					<h2 className="text-lg font-semibold tracking-tight">
						{dashboard.title}
					</h2>
				)}
				<AddTileMenu
					label={`Add a metric to ${dashboard.title ?? "this dashboard"}`}
					addable={addableTiles(graph, dashboard, layout)}
					onAdd={(tileId) => save(addTile(layout, tileId))}
				/>
			</div>
			{tiles.length === 0 ? (
				<p className="text-sm text-muted-foreground">
					No metrics are shown. Add one with the + button.
				</p>
			) : (
				// A list rather than the kit's Grid/Stack, which render a plain div:
				// the tiles are draggable items, and a list is what they are. The
				// classes are those Grid cols={2} and Stack draw with.
				<ul
					className={
						dashboard.layout === "stack"
							? "flex flex-col gap-4"
							: "grid grid-cols-1 gap-4 sm:grid-cols-2"
					}
					onDragOver={onDragOver}
					onDrop={onDrop}
				>
					{tiles}
				</ul>
			)}
		</section>
	);
}

/**
 * Renders every dashboard a registered solution declares in its data-graph slot.
 * Each widget resolves against the audit trail through its own query, so one
 * widget's failure never blanks its siblings. A solution ships only the
 * declaration; all charting lives here in the host. Each viewer arranges each
 * dashboard for themselves, starting from the declared widgets in declared
 * order.
 */
export function SolutionDashboards({
	graph,
	solutionId,
}: {
	graph: DataGraph;
	solutionId: string;
}) {
	return (
		<div className="space-y-8">
			{graph.dashboards.map((dashboard) => (
				<SolutionDashboard
					key={dashboard.id}
					graph={graph}
					dashboard={dashboard}
					solutionId={solutionId}
				/>
			))}
		</div>
	);
}
