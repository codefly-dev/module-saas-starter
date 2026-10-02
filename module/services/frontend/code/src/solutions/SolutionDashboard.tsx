"use client";

import type {
	Dashboard,
	DataGraph,
	MetricWidget,
	WidgetVisualization,
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
	formatShare,
	LineChart,
	SortableBoard,
	type SortableGroupHandle,
	StatChart,
} from "@codefly-dev/ui/dashboard";
import { useQuery } from "@tanstack/react-query";
import { Ellipsis, GripVertical, Info, Pencil, Plus, X } from "lucide-react";
import {
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useId,
	useRef,
	useState,
	useSyncExternalStore,
} from "react";
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
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	Input,
	Popover,
	PopoverContent,
	PopoverTitle,
	PopoverTrigger,
	Skeleton,
} from "@/shared/ui";
import {
	addableTiles,
	addSection,
	addTile,
	canRemoveSection,
	type Layout,
	type LayoutSection,
	layoutKey,
	layoutTiles,
	moveBy,
	moveSection,
	moveSectionBy,
	moveTileToSection,
	newSectionId,
	parseLayout,
	readSavedLayout,
	removeSection,
	removeTile,
	renameSection,
	SECTION_TITLE_MAX,
	sectionTitle,
	serializeLayout,
	swapTiles,
	tileWidget,
	UNSECTIONED,
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

// The tiles about twice as tall as a number: a chart with axes, a bar list or a
// table. In a grid, each spans two rows, so two numbers stack beside it rather
// than one number leaving the rest of the row empty; the grid sizes the rows,
// and every card fills its cell.
const TALL: ReadonlySet<WidgetVisualization> = new Set([
	"line",
	"area",
	"bar",
	"table",
]);

// Dashboard id of the throwaway single-widget graph each card resolves. The card
// resolves its own widget in isolation, so this id is never surfaced.
const SINGLE_WIDGET_DASHBOARD = "widget";

// A graph carrying every event and metric but exactly one widget, so runDashboard
// resolves only that widget's metric (and its derived inputs). Resolving each
// widget through its own call is what isolates failures: a widget whose metric's
// audit query errors fails alone, instead of blanking every sibling that shares
// the dashboard's single batched resolution. The throwaway dashboard declares
// no sections, so its widget names none.
function singleWidgetGraph(graph: DataGraph, widget: MetricWidget): DataGraph {
	return {
		events: graph.events,
		metrics: graph.metrics,
		dashboards: [
			{
				id: SINGLE_WIDGET_DASHBOARD,
				layout: "grid",
				widgets: [{ ...widget, section: undefined }],
			},
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

// What a tile says in place of its values when it has none to draw, and the
// shorter words the + menu says beside its title; null when it has values. The
// tile and the menu both ask, so the menu says a removed tile has nothing to
// show exactly when the tile would.
function withoutValues({
	series,
	visualization,
}: ResolvedWidget): { tile: string; menu: string } | null {
	const partial = series.coverage === "partial";
	if (series.points.length === 0) {
		return partial
			? {
					tile: "Telemetry unavailable or incomplete.",
					menu: "Telemetry incomplete.",
				}
			: { tile: "No data yet.", menu: "No data yet." };
	}
	if (visualization === "number" && series.total === null) {
		return partial
			? {
					tile: "Incomplete telemetry; total unavailable.",
					menu: "Telemetry incomplete.",
				}
			: {
					tile: "Total unavailable across groups.",
					menu: "Total unavailable.",
				};
	}
	return null;
}

function WidgetValues({ widget }: { widget: ResolvedWidget }) {
	const { series, visualization } = widget;
	// A metric declared as a percent is a share from 0 to 1, written as a
	// percentage everywhere the tile shows a value; a plain number keeps each
	// chart's own default.
	const formatValue = widget.format === "percent" ? formatShare : undefined;
	const missing = withoutValues(widget);
	if (missing) {
		return <p className="text-sm text-muted-foreground">{missing.tile}</p>;
	}
	switch (visualization) {
		case "line":
			return (
				<LineChart
					points={series.points}
					className="text-primary/70"
					axes
					formatValue={formatValue}
				/>
			);
		case "area":
			return (
				<AreaChart
					points={series.points}
					className="text-primary/70"
					axes
					formatValue={formatValue}
				/>
			);
		case "bar":
			return <BarList points={series.points} formatValue={formatValue} />;
		case "number":
			// A number with no total has said so above; this only narrows it.
			return series.total === null ? null : (
				<StatChart
					total={series.total}
					points={series.points}
					formatValue={formatValue}
				/>
			);
		case "table":
			return (
				<table className="w-full text-sm">
					<tbody>
						{series.points.map((point) => (
							<tr key={point.key} className="border-b last:border-0">
								<td className="py-1 text-muted-foreground">{point.key}</td>
								<td className="py-1 text-right font-mono">
									{formatValue
										? formatValue(point.value)
										: point.value.toLocaleString()}
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

// Where a dashboard's widgets resolve: the solution's graph, and whose
// dashboard it is.
interface WidgetSource {
	graph: DataGraph;
	solutionId: string;
	dashboardId: string;
	orgId: string;
}

// One widget's resolution. The tile asks for it when drawn, and a + menu for
// each removed widget while it is open, with the same key: a tile added back
// from the menu draws from the answer the menu got.
function useWidgetSeries(
	{ graph, solutionId, dashboardId, orgId }: WidgetSource,
	widget: MetricWidget,
	enabled = true,
) {
	// The graph is part of the key so a solution that redeploys with a changed
	// declaration refetches instead of serving another graph's cached series that
	// happens to share the same solution/dashboard/widget/org ids. Disabled until
	// the org resolves so the pre-org window reads as loading, never as empty.
	return useQuery({
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
		enabled: enabled && orgId !== "",
	});
}

function WidgetCard({
	graph,
	widget,
	solutionId,
	dashboardId,
	orgId,
	grip,
	info,
	remove,
}: {
	graph: DataGraph;
	widget: MetricWidget;
	solutionId: string;
	dashboardId: string;
	orgId: string;
	grip: ReactNode;
	info: ReactNode;
	remove: ReactNode;
}) {
	const { data, isPending, isError } = useWidgetSeries(
		{ graph, solutionId, dashboardId, orgId },
		widget,
	);

	return (
		<Card className="h-full">
			<CardHeader className="flex flex-row items-center gap-1 pb-2">
				{grip}
				<CardTitle className="min-w-0 flex-1 text-base">
					{widget.title ?? widget.metric}
				</CardTitle>
				{info}
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
	dashboard: Dashboard,
): [Layout, (next: Layout) => void] {
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
	const save = (next: Layout) => {
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

// Sections are one column, so only up and down move one.
const SECTION_STEP: Partial<Record<string, number>> = {
	ArrowUp: -1,
	ArrowDown: 1,
};

// The section a tile is in, and its place there.
function placeOf(
	layout: Layout,
	tileId: string,
): { section: LayoutSection; index: number } | undefined {
	const section = layout.find(({ tiles }) => tiles.includes(tileId));
	return section && { section, index: section.tiles.indexOf(tileId) };
}

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

// A removed widget in a + menu, and, beside its title, what its tile would say
// in place of values it does not have. It asks the tile's own query, only while
// the menu is open. Until there is an answer, and when the query fails, it says
// nothing: a failure is not "no data".
function AddableTile({
	source,
	widget,
	label,
	open,
	onAdd,
}: {
	source: WidgetSource;
	widget: MetricWidget;
	label: string;
	open: boolean;
	onAdd: () => void;
}) {
	const { data, isError } = useWidgetSeries(source, widget, open);
	const hint = isError || !data ? undefined : withoutValues(data)?.menu;
	return (
		<DropdownMenuItem hint={hint} onClick={onAdd}>
			{label}
		</DropdownMenuItem>
	);
}

// Lists what the viewer can put back on the dashboard: the declared widgets
// they removed. A preference never adds a metric the dashboard does not draw
// (ADR 0007). Each section has its own, which puts the widget back there.
function AddTileMenu({
	label,
	source,
	addable,
	onAdd,
}: {
	label: string;
	source: WidgetSource;
	addable: { id: string; label: string; widget: MetricWidget }[];
	onAdd: (tileId: string) => void;
}) {
	// Nothing is asked about a removed widget until the viewer opens the menu.
	const [open, setOpen] = useState(false);
	return (
		<DropdownMenu open={open} onOpenChange={setOpen}>
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
						<AddableTile
							key={tile.id}
							source={source}
							widget={tile.widget}
							label={tile.label}
							open={open}
							onAdd={() => onAdd(tile.id)}
						/>
					))
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

// A section's title while the viewer renames it, focused with the title
// selected. Enter or leaving the field keeps what was typed; Escape keeps the
// title as it was. `refocus` says the viewer is still here (they pressed a
// key), so focus goes back to where the rename began.
function SectionTitleInput({
	title,
	onDone,
}: {
	title: string;
	onDone: (typed: string | null, refocus: boolean) => void;
}) {
	const input = useRef<HTMLInputElement>(null);
	// Leaving the field after Enter or Escape is not a second answer.
	const done = useRef(false);
	const finish = (typed: string | null, refocus: boolean) => {
		if (done.current) return;
		done.current = true;
		onDone(typed, refocus);
	};
	useEffect(() => {
		input.current?.focus();
		input.current?.select();
	}, []);
	return (
		<Input
			ref={input}
			aria-label="Section title"
			defaultValue={title}
			maxLength={SECTION_TITLE_MAX}
			className="max-w-xs font-semibold"
			onKeyDown={(event) => {
				if (event.key === "Enter") {
					event.preventDefault();
					finish(event.currentTarget.value, true);
				} else if (event.key === "Escape") {
					event.preventDefault();
					event.stopPropagation();
					finish(null, true);
				}
			}}
			onBlur={(event) => finish(event.currentTarget.value, false)}
		/>
	);
}

// A section's options: rename it, or remove it. The last section left cannot
// be removed.
function SectionMenu({
	sectionId,
	title,
	removable,
	onRename,
	onRemove,
}: {
	sectionId: string;
	title: string;
	removable: boolean;
	onRename: () => void;
	onRemove: () => void;
}) {
	// Renaming puts a focused field in the title's place. The menu hands the
	// focus back to its trigger only while nothing else has taken it, so the
	// field keeps it; an explicit `finalFocus` would take it back regardless.
	return (
		<DropdownMenu>
			<DropdownMenuTrigger
				render={
					<Button
						type="button"
						variant="ghost"
						size="icon-sm"
						className="text-muted-foreground"
						aria-label={`Section options for ${title}`}
						data-section-options={sectionId}
					/>
				}
			>
				<Ellipsis />
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem onClick={onRename}>Rename section</DropdownMenuItem>
				<DropdownMenuItem disabled={!removable} onClick={onRemove}>
					Remove section
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

// A section's heading: the grip it is dragged by, its title (or the field
// that renames it), what it is for, its own + menu and its options. The title
// is itself the way to rename the section, with a pencil on hover or focus to
// say so; the heading is named by the title alone. Only the grip drags.
function SectionHeader({
	sectionId,
	grip,
	title,
	description,
	empty,
	rename,
	onRename,
	add,
	options,
}: {
	sectionId: string;
	grip: ReactNode;
	title: string;
	description?: string;
	empty: boolean;
	rename: ReactNode;
	onRename: () => void;
	add: ReactNode;
	options: ReactNode;
}) {
	const titleId = useId();
	return (
		<div className="mb-3 flex items-start gap-2">
			{grip}
			<div className="min-w-0">
				{rename ?? (
					<h3 className="font-semibold" aria-labelledby={titleId}>
						<button
							type="button"
							aria-label={`Rename section ${title}`}
							data-section-title={sectionId}
							className="group/title inline-flex max-w-full cursor-text items-center gap-1.5 rounded-sm text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
							onClick={onRename}
						>
							<span id={titleId} className="min-w-0 break-words">
								{title}
							</span>
							<Pencil
								aria-hidden
								className="size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/title:opacity-100 group-focus-visible/title:opacity-100"
							/>
						</button>
					</h3>
				)}
				{description && (
					<p className="text-sm text-muted-foreground">{description}</p>
				)}
				{empty && (
					<p className="text-sm text-muted-foreground">
						No widgets in this section. Drag a tile here, or add one back with
						+.
					</p>
				)}
			</div>
			<div className="ml-auto flex items-center gap-1">
				{add}
				{options}
			</div>
		</div>
	);
}

// What follows the pointer while a section is dragged: its title and what it
// is for, not its tiles. Not a heading: the section's own heading stays where
// it is meanwhile.
function SectionPreview({
	title,
	description,
}: {
	title: string;
	description?: string;
}) {
	return (
		<div className="rounded-lg bg-background p-3 ring-1 ring-foreground/10">
			<p className="font-semibold">{title}</p>
			{description && (
				<p className="text-sm text-muted-foreground">{description}</p>
			)}
		</div>
	);
}

// The name a screen reader gives the untitled section of a dashboard that
// declares none, once the viewer has added sections beside it.
const UNTITLED_SECTION = "the untitled section";

// A dashboard the viewer can arrange: drag a tile onto another to swap the two
// (or focus its grip and use the arrow keys), × to remove one, + to add one
// back. On a dashboard with sections a tile can be dragged to another section,
// or onto the slot at the end of one. The viewer can rename, reorder or remove
// a section, and add their own after the last; a section of their own starts
// empty and holds only declared widgets.
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
	const root = useRef<HTMLElement>(null);
	const storageKey = scopedDashboardDraftKey(
		layoutKey(solutionId, dashboard.id),
		{ organizationId, userId: user?.id },
	);
	const [layout, save] = useDashboardLayout(storageKey, dashboard);
	const sections = dashboard.sections ?? [];
	const shown = layoutTiles(layout).flatMap((tileId) => {
		const widget = tileWidget(dashboard, tileId);
		return widget ? [{ tileId, widget }] : [];
	});
	const widgets = new Map(shown.map(({ tileId, widget }) => [tileId, widget]));
	const source = { graph, solutionId, dashboardId: dashboard.id, orgId };
	const addable = addableTiles(graph, dashboard, layout).flatMap((tile) => {
		const widget = tileWidget(dashboard, tile.id);
		return widget ? [{ ...tile, widget }] : [];
	});
	// The section whose title is a field, while the viewer renames it, and
	// where the focus goes back to once they are done: the options menu the
	// rename was chosen from, or the title.
	const [renaming, setRenaming] = useState<{
		id: string;
		back: "data-section-options" | "data-section-title";
	} | null>(null);
	// What a screen reader last heard about the sections: an add, a rename or
	// a removal. (The board announces a tile's moves itself.)
	const [announcement, setAnnouncement] = useState("");
	const titleOf = (tileId: string) => {
		const widget = widgets.get(tileId);
		return widget?.title ?? widget?.metric ?? tileId;
	};
	const nameOf = (section: LayoutSection) =>
		sectionTitle(dashboard, section) ?? UNTITLED_SECTION;
	// A tile moved into another section is drawn anew there, so its grip is
	// found again rather than kept; so is a section's options button.
	const marked = (attribute: string, id: string) =>
		[
			...(root.current?.querySelectorAll<HTMLElement>(`[${attribute}]`) ?? []),
		].find((element) => element.getAttribute(attribute) === id);
	const gripOf = (tileId: string) => marked("data-tile-grip", tileId);
	const addNewSection = () => {
		const id = newSectionId(layout);
		save(addSection(layout, id));
		setRenaming({ id, back: "data-section-title" });
		setAnnouncement("Added a section at the end.");
	};
	const finishRename = (
		section: LayoutSection,
		typed: string | null,
		refocus: boolean,
	) => {
		const next =
			typed === null
				? layout
				: renameSection(layout, dashboard, section.id, typed);
		const before = nameOf(section);
		const after = next.find(({ id }) => id === section.id);
		const back = renaming?.back ?? "data-section-title";
		// The field goes away, which drops its focus; commit first so the
		// title or the options can take it back.
		flushSync(() => {
			setRenaming(null);
			if (after && nameOf(after) !== before) {
				save(next);
				setAnnouncement(`Renamed ${before} to ${nameOf(after)}.`);
			}
		});
		if (refocus) marked(back, section.id)?.focus();
	};
	// The section grip's arrow keys move the section one place up or down.
	const moveSectionWithKey = (
		section: LayoutSection,
		event: KeyboardEvent<HTMLElement>,
	) => {
		const step = SECTION_STEP[event.key];
		if (step === undefined) return;
		event.preventDefault();
		const next = moveSectionBy(layout, section.id, step);
		const at = next.findIndex(({ id }) => id === section.id);
		if (at === layout.indexOf(section)) return;
		const neighbour = step < 0 ? next[at + 1] : next[at - 1];
		// Moving a section moves its DOM node, which drops focus; commit first
		// so the grip can take focus back.
		flushSync(() => {
			save(next);
			setAnnouncement(
				`${nameOf(section)} moved ${step < 0 ? "above" : "below"} ${nameOf(neighbour)}.`,
			);
		});
		marked("data-section-grip", section.id)?.focus();
	};
	const remove = (section: LayoutSection) => {
		const at = layout.indexOf(section);
		const into = layout[at - 1] ?? layout[at + 1];
		if (!into || !canRemoveSection(layout, section.id)) return;
		const moved = section.tiles.some((tileId) => widgets.has(tileId));
		// The section's options go with it; the focus goes to the section that
		// took its tiles, or to "Add section" when that one has no options. The
		// menu, unmounted with the section, queues handing the focus back
		// somewhere as it goes, so this waits until after it.
		flushSync(() => {
			save(removeSection(layout, section.id));
			setAnnouncement(
				moved
					? `Removed ${nameOf(section)}; its widgets moved to ${nameOf(into)}.`
					: `Removed ${nameOf(section)}.`,
			);
		});
		queueMicrotask(() =>
			(
				marked("data-section-options", into.id) ??
				root.current?.querySelector<HTMLElement>("[data-add-section]")
			)?.focus(),
		);
	};
	const addMenu = (label: string, sectionId: string) => (
		<AddTileMenu
			label={label}
			source={source}
			addable={addable}
			onAdd={(tileId) => save(addTile(layout, dashboard, tileId, sectionId))}
		/>
	);
	const renderTile = (tileId: string) => {
		const widget = widgets.get(tileId);
		if (!widget) return null;
		const title = titleOf(tileId);
		return (
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
						data-tile-grip={tileId}
						onKeyDown={(event) => {
							const step = ARROW_STEP[event.key];
							if (step === undefined) return;
							event.preventDefault();
							const next = moveBy(layout, tileId, step);
							const from = placeOf(layout, tileId);
							const to = placeOf(next, tileId);
							// Past either end of the dashboard the tile stays, and a
							// key that moves nothing saves nothing.
							if (
								!from ||
								!to ||
								(to.section.id === from.section.id && to.index === from.index)
							) {
								return;
							}
							// Moving a tile can move its DOM node, which drops focus;
							// commit first so the grip can take focus back.
							flushSync(() => {
								save(next);
								if (to.section.id !== from.section.id) {
									setAnnouncement(
										`${title} moved to the ${step < 0 ? "end" : "start"} of ${nameOf(to.section)}.`,
									);
								}
							});
							gripOf(tileId)?.focus();
						}}
					>
						<GripVertical />
					</Button>
				}
				info={<MetricInfo graph={graph} widget={widget} orgId={orgId} />}
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
				<WidgetCard
					graph={graph}
					widget={widget}
					solutionId={solutionId}
					dashboardId={dashboard.id}
					orgId={orgId}
					grip={glyph(<GripVertical />)}
					info={glyph(<Info />)}
					remove={glyph(<X />)}
				/>
			</div>
		);
	};

	return (
		<section ref={root} className="space-y-4">
			<div className="flex items-center gap-2">
				{dashboard.title && (
					<h2 className="text-lg font-semibold tracking-tight">
						{dashboard.title}
					</h2>
				)}
				{/* A dashboard with sections has a + in every section instead. */}
				{sections.length === 0 &&
					addMenu(
						`Add a widget back to ${dashboard.title ?? "this dashboard"}`,
						UNSECTIONED,
					)}
			</div>
			{sections.length === 0 && layout.length === 1 && shown.length === 0 ? (
				<p className="text-sm text-muted-foreground">
					No widgets are shown. Add one back with the + button.
				</p>
			) : (
				// The kit's SortableBoard rather than its Grid/Stack: the tiles are
				// dragged onto each other to swap, and onto another section. The
				// classes are those Grid cols={2} and Stack draw with. The sections
				// draw in the viewer's order, the first on top, each dragged by the
				// grip in its header; a dashboard without sections is one untitled
				// one, which stays first.
				<SortableBoard
					className="space-y-6"
					groups={layout.map((placed) => {
						const declared = sections.find(({ id }) => id === placed.id);
						const title = sectionTitle(dashboard, placed);
						const ids = placed.tiles.filter((tileId) => widgets.has(tileId));
						return {
							id: placed.id,
							ids,
							label: nameOf(placed),
							fixed: placed.id === UNSECTIONED,
							preview:
								title === undefined ? undefined : (
									<SectionPreview
										title={title}
										description={declared?.description}
									/>
								),
							header:
								title === undefined
									? undefined
									: (handle: SortableGroupHandle) => (
											<SectionHeader
												sectionId={placed.id}
												grip={
													<Button
														type="button"
														variant="ghost"
														size="icon-xs"
														className="mt-0.5 cursor-grab text-muted-foreground"
														aria-label={`Move ${title}: drag the section, or use the arrow keys`}
														data-section-grip={placed.id}
														{...handle}
														onKeyDown={(event) =>
															moveSectionWithKey(placed, event)
														}
													>
														<GripVertical />
													</Button>
												}
												title={title}
												description={declared?.description}
												empty={ids.length === 0}
												rename={
													renaming?.id === placed.id ? (
														<SectionTitleInput
															title={title}
															onDone={(typed, refocus) =>
																finishRename(placed, typed, refocus)
															}
														/>
													) : undefined
												}
												onRename={() =>
													setRenaming({
														id: placed.id,
														back: "data-section-title",
													})
												}
												add={addMenu(
													`Add a widget back to ${title}`,
													placed.id,
												)}
												options={
													<SectionMenu
														sectionId={placed.id}
														title={title}
														removable={canRemoveSection(layout, placed.id)}
														onRename={() =>
															setRenaming({
																id: placed.id,
																back: "data-section-options",
															})
														}
														onRemove={() => remove(placed)}
													/>
												}
											/>
										),
							className:
								dashboard.layout === "stack"
									? "flex flex-col gap-4"
									: "grid grid-cols-1 gap-4 sm:grid-cols-2",
						};
					})}
					onSwap={(dragged, target) => save(swapTiles(layout, dragged, target))}
					onMove={
						sections.length > 0 || layout.length > 1
							? (dragged, sectionId) =>
									save(moveTileToSection(layout, dragged, sectionId))
							: undefined
					}
					onMoveGroup={(sectionId, index) =>
						save(moveSection(layout, sectionId, index))
					}
					itemLabel={titleOf}
					itemClassName={(tileId) => {
						const widget = widgets.get(tileId);
						return widget && TALL.has(widget.visualization)
							? "row-span-2"
							: undefined;
					}}
					renderItem={renderTile}
					renderOverlay={renderDragged}
				/>
			)}
			<Button
				type="button"
				variant="outline"
				size="sm"
				data-add-section=""
				onClick={addNewSection}
			>
				<Plus />
				Add section
			</Button>
			<p role="status" className="sr-only">
				{announcement}
			</p>
		</section>
	);
}

/**
 * Renders every dashboard a registered solution declares in its data-graph slot.
 * Each widget resolves against the audit trail through its own query, so one
 * widget's failure never blanks its siblings. A solution ships only the
 * declaration; all charting lives here in the host. Each viewer arranges each
 * dashboard for themselves, starting from the declared widgets in declared
 * order, each in its declared section.
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
