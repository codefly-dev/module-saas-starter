"use client";

import type {
	Dashboard as DashboardDeclaration,
	DataGraph,
	MetricWidget,
} from "@codefly/saas-plugin-manifest";
import { createSaasClient, runDashboard } from "@codefly-dev/saas-sdk";
// The dashboard is DRAWN by the shared kit's `<Dashboard>` — the same renderer,
// from the same package instance, that a solution's own remote mounts and that
// the host's operations pages are built from. Nothing here re-implements it:
// this file owns only what is genuinely the host's (the per-widget audit
// queries, the viewer's saved arrangement of the tiles, and the ⓘ that says
// where a number comes from) and composes it AROUND the renderer through the
// renderer's own slots.
import {
	Dashboard,
	DashboardWidget,
	type DashboardWidgetView,
	SortableGrid,
	type WidgetReadFailure,
} from "@codefly-dev/ui/dashboard";
import {
	type QueryClient,
	useQuery,
	useQueryClient,
} from "@tanstack/react-query";
import { GripVertical, Info, Plus, X } from "lucide-react";
import { type ReactNode, useState, useSyncExternalStore } from "react";
import { flushSync } from "react-dom";
import { resolveActor } from "@/features/audit/model/transforms";
import {
	useAuditEventTypes,
	usePrincipalDirectory,
} from "@/features/audit/service/queries";
import { scopedDashboardDraftKey } from "@/features/dashboard/service/use-dashboard-authoring";
import { useAuth } from "@/lib/auth";
import { apiTransport } from "@/lib/connect/transport";
import {
	mayKeepRetainedRows,
	type ReadOutcome,
	readOutcome,
} from "@/shared/lib/read-outcome";
import {
	Button,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	Popover,
	PopoverContent,
	PopoverTitle,
	PopoverTrigger,
} from "@/shared/ui";
import {
	addableTiles,
	addTile,
	layoutKey,
	moveBy,
	parseLayout,
	readSavedLayout,
	removeTile,
	serializeLayout,
	swapTiles,
	tileWidget,
	writeSavedLayout,
} from "./dashboard-layout";
import {
	ACTIVITY_DASHBOARD,
	activityGraph,
	activitySummary,
	describeMetric,
	formatDay,
	formatMoment,
	newestEvents,
	recentEventsQueries,
} from "./metric-info";

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

// One declared widget, resolved and drawn.
//
// The query lives here, in a component of its own, because each widget is
// resolved in isolation: a widget whose metric's audit query errors fails
// alone instead of blanking every sibling that would share one batched
// resolution. React fixes the number of hooks a component may call, so these
// queries cannot be hoisted into the renderer that takes the whole view —
// which is why the kit exports `DashboardWidget` beside `<Dashboard>` and this
// mounts that, rather than drawing a card of its own.
function SolutionWidget({
	graph,
	widget,
	solutionId,
	dashboardId,
	orgId,
	actions,
}: {
	graph: DataGraph;
	widget: MetricWidget;
	solutionId: string;
	dashboardId: string;
	orgId: string;
	actions: ReactNode;
}) {
	// The graph is part of the key so a solution that redeploys with a changed
	// declaration refetches instead of serving another graph's cached series that
	// happens to share the same solution/dashboard/widget/org ids. Disabled until
	// the org resolves so the pre-org window reads as loading, never as empty.
	const client = useQueryClient();
	const source = widgetSource({ solutionId, dashboardId, widget, orgId });
	const { data, isError, error } = useQuery({
		queryKey: [...source, graph],
		queryFn: () =>
			runDashboard(
				sdk.audit,
				singleWidgetGraph(graph, widget),
				SINGLE_WIDGET_DASHBOARD,
				{ orgId },
			)
				.then((resolved) => resolved.widgets[0])
				.catch((cause: unknown) => {
					// The read itself answers for what it leaves behind. A refused
					// read deletes every snapshot of this widget's source before the
					// rejection reaches anyone, so there is nothing retained for a
					// later error to hand back and nothing to keep in step with it.
					if (!mayKeepRetainedRows(readOutcome(true, cause))) {
						discardSnapshots(client, source);
					}
					throw cause;
				}),
		enabled: orgId !== "",
	});

	const outcome = readOutcome(isError, error);
	// Whatever the cache still holds. A refusal has already emptied it, which is
	// why this reads the snapshot without asking whether the viewer may keep it:
	// the question was put at the read boundary, where the answer could be acted
	// on once for every revision instead of per mounted component.
	const retained = data?.series ?? null;

	return (
		<DashboardWidget
			widget={{
				...declaredWidget(widget),
				series: retained,
				failure: WIDGET_FAILURE[outcome],
			}}
			actions={actions}
		/>
	);
}

/**
 * Every cached revision of one widget's source — the query key without the
 * declaration that produced it.
 *
 * A dashboard whose declaration has changed since keeps its own cache entry: a
 * different title is enough for another key, holding the same numbers. So a
 * rule about this widget's values is a rule about the whole prefix, never about
 * the one entry a render happens to have resolved.
 */
function widgetSource({
	solutionId,
	dashboardId,
	widget,
	orgId,
}: {
	solutionId: string;
	dashboardId: string;
	widget: MetricWidget;
	orgId: string;
}): readonly unknown[] {
	return ["solution-widget", solutionId, dashboardId, widget.id, orgId];
}

/**
 * Delete the retained snapshots under a widget's source: the values go, the
 * queries and their states stay.
 *
 * This is what `mayKeepRetainedRows` says must happen to rows a refused read
 * leaves behind, done where the refusal is classified rather than in a
 * component. The difference is not tidiness. A component effect runs only for
 * the revision that is mounted and only while it is: a refusal that arrives
 * after the viewer switched dashboards, or after the page unmounted, left the
 * numbers in the cache for the next render to serve. The read is the one thing
 * that happens exactly once per refusal, whoever is watching.
 *
 * Writing the state is the whole of it, deliberately:
 *
 *   - `removeQueries` destroys the entry, which silently cancels the very read
 *     that is reporting the refusal (`Query.destroy` → `cancel({silent:true})`)
 *     and leaves a mounted observer holding a detached query that no longer
 *     re-renders — the values stay on screen and recovery has nothing to
 *     refetch into.
 *   - `setQueryData(key, undefined)` cannot express this at all: the client
 *     returns early when the new data is `undefined`, so the call does nothing.
 *   - `resetQueries` clears the error with the data and refetches every active
 *     entry, so a standing refusal re-asks and re-clears without end.
 *
 * Setting `data` notifies the observers, cancels nothing, keeps the refusal,
 * and starts no fetch. An entry left with no data renders as a wait and is
 * stale, so the next read of it is a real read — which is the only way values
 * come back.
 */
function discardSnapshots(client: QueryClient, source: readonly unknown[]) {
	for (const query of client
		.getQueryCache()
		.findAll({ queryKey: source, exact: false })) {
		if (query.state.data === undefined) continue;
		query.setState({ data: undefined, dataUpdatedAt: 0 });
	}
}

// The host's read classification as the renderer's presentational one. A read
// that succeeded is no failure at all, which is what `undefined` says.
const WIDGET_FAILURE: Record<ReadOutcome, WidgetReadFailure | undefined> = {
	empty: undefined,
	forbidden: "refused",
	failed: "failed",
};

// A declared widget as the renderer's view model, with no series yet. The
// series is bound by `SolutionWidget`, the one place that has resolved it; a
// `null` series is the honest value here and the renderer draws it as a wait
// rather than as an empty result.
function declaredWidget(widget: MetricWidget): DashboardWidgetView {
	return {
		id: widget.id,
		visualization: widget.visualization,
		title: widget.title ?? widget.metric,
		series: null,
	};
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
 * customizes keeps following the declared default.
 *
 * STOPGAP (labelled): browser storage is an interim home, so a layout does not
 * follow the viewer to another browser or device. ADR 0007 puts per-user
 * dashboard preferences in a composed `user_settings` field keyed by dashboard
 * id; that field and its typed settings-catalog entry do not exist yet (no
 * tracking issue yet). When they land, this hook's read and write move there
 * and nothing else changes.
 */
function useDashboardLayout(
	storageKey: string,
	dashboard: DashboardDeclaration,
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

// How many of the newest events the ⓘ lists.
const RECENT_EVENTS = 5;

// Where a tile's number comes from: what it counts and from which audit
// events, read from the solution's declaration; how many events there are and
// the days they span, from per-day counts; and the newest events themselves,
// from the audit log. Nothing is asked of the audit trail until the viewer
// opens it.
function MetricInfo({
	graph,
	widget,
	orgId,
}: {
	graph: DataGraph;
	widget: MetricWidget;
	orgId: string;
}) {
	const [open, setOpen] = useState(false);
	const title = widget.title ?? widget.metric;
	const { counts, sources, note } = describeMetric(
		graph,
		widget.metric,
		widget.visualization,
	);
	const { data: registered } = useAuditEventTypes({ enabled: open });
	const activity = useQuery({
		queryKey: ["solution-metric-activity", widget.metric, orgId, graph],
		queryFn: () =>
			runDashboard(
				sdk.audit,
				activityGraph(graph, widget.metric),
				ACTIVITY_DASHBOARD,
				{ orgId },
			).then((resolved) =>
				activitySummary(resolved.widgets.map((w) => w.series)),
			),
		enabled: open && orgId !== "",
	});
	const searches = recentEventsQueries(graph, widget.metric);
	const recent = useQuery({
		queryKey: ["solution-metric-recent", widget.metric, orgId, graph],
		queryFn: async () =>
			newestEvents(
				await Promise.all(
					(searches ?? []).map((search) =>
						sdk.audit
							.queryAuditLog({ orgId, pageSize: RECENT_EVENTS, ...search })
							.then((response) => response.events),
					),
				),
				RECENT_EVENTS,
			),
		enabled: open && orgId !== "" && searches !== null,
	});
	const { directory } = usePrincipalDirectory(
		orgId,
		recent.data?.map((event) => event.actorId) ?? [],
	);
	const who = (actorId: string) => resolveActor(actorId, directory).label;

	const summary = activity.data;
	const counted = activity.isPending
		? "Loading…"
		: activity.isError
			? "Unavailable"
			: null;
	const dayFreshness =
		counted ??
		(summary ? `Latest event on ${formatDay(summary.last)}` : "No events yet");
	// A metric the audit log search cannot narrow like the metric does (by
	// collection) keeps the day its per-day counts give.
	const newest = recent.data?.[0];
	const freshness =
		searches === null || recent.isError
			? dayFreshness
			: recent.isPending
				? "Loading…"
				: newest
					? `${formatMoment(newest.at)}, by ${who(newest.actorId)}`
					: "No events yet";

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger
				render={
					<Button
						type="button"
						variant="ghost"
						size="icon-xs"
						className="text-muted-foreground"
						aria-label={`About ${title}`}
					/>
				}
			>
				<Info />
			</PopoverTrigger>
			<PopoverContent align="end" className="w-96">
				<PopoverTitle>{title}</PopoverTitle>
				<dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-2">
					<dt className="text-muted-foreground">Counts</dt>
					<dd>{counts}</dd>
					<dt className="text-muted-foreground">Counted from</dt>
					<dd className="space-y-1">
						{sources.map(({ type, declared }) => {
							// The audit service's type list holds only the platform's own
							// types, so a type a module registers falls back to what the
							// solution's declaration says about it.
							const description =
								registered?.find((t) => t.name === type)?.description ||
								declared;
							return (
								<p key={type}>
									{description && <span className="block">{description}</span>}
									<code className="font-mono text-xs text-muted-foreground">
										{type}
									</code>
								</p>
							);
						})}
					</dd>
					<dt className="text-muted-foreground">Scope</dt>
					<dd>Your organization</dd>
					<dt className="text-muted-foreground">Period</dt>
					<dd>All time</dd>
					<dt className="text-muted-foreground">Based on</dt>
					<dd>
						{counted ??
							(summary
								? `${summary.events.toLocaleString()} ${summary.events === 1 ? "event" : "events"}`
								: "No events yet")}
					</dd>
					<dt className="text-muted-foreground">Data range</dt>
					<dd>
						{counted ??
							(summary
								? formatDay(summary.first) === formatDay(summary.last)
									? formatDay(summary.first)
									: `${formatDay(summary.first)} – ${formatDay(summary.last)}`
								: "No events yet")}
					</dd>
					<dt className="text-muted-foreground">Freshness</dt>
					<dd>{freshness}</dd>
					{recent.data && recent.data.length > 0 && (
						<>
							<dt className="text-muted-foreground">Recent events</dt>
							<dd>
								<ul className="space-y-0.5">
									{recent.data.map((event) => (
										<li key={event.id}>
											{formatMoment(event.at, { year: false })} ·{" "}
											{who(event.actorId)}
										</li>
									))}
								</ul>
							</dd>
						</>
					)}
					{note && (
						<>
							<dt className="text-muted-foreground">Note</dt>
							<dd>{note}</dd>
						</>
					)}
				</dl>
			</PopoverContent>
		</Popover>
	);
}

// Lists what the viewer can put back on the dashboard: the declared widgets
// they removed. A preference never adds a metric the dashboard does not draw
// (ADR 0007).
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
						Every widget is on this dashboard
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
	dashboard: DashboardDeclaration;
	solutionId: string;
}) {
	const { user, organizationId } = useAuth();
	const orgId = organizationId ?? "";
	const storageKey = scopedDashboardDraftKey(
		layoutKey(solutionId, dashboard.id),
		{ organizationId, userId: user?.id },
	);
	const [layout, save] = useDashboardLayout(storageKey, dashboard);
	const shown = layout.flatMap((tileId) => {
		const widget = tileWidget(dashboard, tileId);
		return widget ? [{ tileId, widget }] : [];
	});
	const widgets = new Map(shown.map(({ tileId, widget }) => [tileId, widget]));
	const titleOf = (tileId: string) => {
		const widget = widgets.get(tileId);
		return widget?.title ?? widget?.metric ?? tileId;
	};
	// The grip, the ⓘ and the × — the three host-only controls — go into the
	// renderer's own per-widget actions slot, so they sit in the card header the
	// kit draws instead of a header this file draws.
	const controls = (tileId: string) => {
		const title = titleOf(tileId);
		const widget = widgets.get(tileId);
		return (
			<>
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
				{widget && <MetricInfo graph={graph} widget={widget} orgId={orgId} />}
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
			</>
		);
	};
	// The floating copy while a tile is dragged: the same card, but with no
	// info popover (so no second panel or audit query exists mid-drag) and no
	// controls a keyboard or screen reader could reach. The icons stay so the
	// copy is drawn the same size as the tile it follows.
	const renderDragged = (tileId: string) => {
		const widget = widgets.get(tileId);
		if (!widget) return null;
		const glyph = (icon: ReactNode) => (
			<Button
				type="button"
				variant="ghost"
				size="icon-xs"
				className="text-muted-foreground"
				tabIndex={-1}
			>
				{icon}
			</Button>
		);
		return (
			<div inert aria-hidden="true">
				<SolutionWidget
					graph={graph}
					widget={widget}
					solutionId={solutionId}
					dashboardId={dashboard.id}
					orgId={orgId}
					actions={
						<>
							{glyph(<GripVertical />)}
							{glyph(<Info />)}
							{glyph(<X />)}
						</>
					}
				/>
			</div>
		);
	};

	return (
		<Dashboard
			data={{
				title: dashboard.title,
				layout: dashboard.layout,
				widgets: shown.map(({ widget }) => declaredWidget(widget)),
			}}
			slots={{
				actions: (
					<AddTileMenu
						label={`Add a widget back to ${dashboard.title ?? "this dashboard"}`}
						addable={addableTiles(graph, dashboard, layout)}
						onAdd={(tileId) => save(addTile(layout, tileId))}
					/>
				),
				renderWidget: (view) => {
					const widget = widgets.get(view.id);
					if (!widget) return null;
					return (
						<SolutionWidget
							graph={graph}
							widget={widget}
							solutionId={solutionId}
							dashboardId={dashboard.id}
							orgId={orgId}
							actions={controls(view.id)}
						/>
					);
				},
				// The kit's SortableGrid rather than its default grid: the tiles
				// are dragged onto each other to swap. The classes are those the
				// kit's grid and stack draw with.
				layout: (tiles) => {
					const nodes = new Map(tiles.map((tile) => [tile.id, tile.node]));
					return (
						<SortableGrid
							ids={tiles.map((tile) => tile.id)}
							onSwap={(dragged, target) =>
								save(swapTiles(layout, dragged, target))
							}
							itemLabel={titleOf}
							renderItem={(id) => nodes.get(id) ?? null}
							renderOverlay={renderDragged}
							className={
								dashboard.layout === "stack"
									? "flex flex-col gap-4"
									: "grid grid-cols-1 gap-4 sm:grid-cols-2"
							}
						/>
					);
				},
				empty: (
					<p className="type-body text-muted-foreground">
						No widgets are shown. Add one back with the + button.
					</p>
				),
			}}
		/>
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
