"use client";

import { Download } from "lucide-react";
import { useMemo, useState } from "react";
import { Dashboard, type DashboardData } from "@/components/dashboard";
import { useAuth } from "@/lib/auth";
import {
	Button,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	SegmentedControl,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import {
	AUDIT_BUCKETS,
	AUDIT_RANGE_PRESETS,
	type AuditBucket,
	type AuditRangePreset,
	auditWindows,
	byEventType,
	countEventTypes,
	defaultBucketFor,
	distinctCount,
	newUserEventTypes,
	pivotByTime,
	relativeChange,
	SECURITY_CATEGORY,
	sliceCategory,
	topGroups,
	totalCount,
} from "../model/analytics";
import { formatAuditAction } from "../model/transforms";
import { useExportAuditLog } from "../service/mutations";
import {
	type AuditGroupDimension,
	useAuditAggregate,
	useAuditEventTypes,
	useAuditLog,
	usePrincipalDirectory,
} from "../service/queries";
import { AuditTable } from "./audit-table";

export function AuditPage() {
	const [eventTypeFilter, setEventTypeFilter] = useState("all");
	const [categoryFilter, setCategoryFilter] = useState("all");
	const [namespaceFilter, setNamespaceFilter] = useState("all");
	// A filter the page holds but the dropdowns cannot express: a SET of event
	// types, which is what a tile summarising a family of them opens. null means
	// no set is selected; the dropdowns clear it when they change, because a
	// category drill-down would silently change what the tile counted.
	const [eventTypeSet, setEventTypeSet] = useState<string[] | null>(null);
	const [range, setRange] = useState<AuditRangePreset>("30d");
	// The bucket follows the range until the viewer picks one: 90 days at a
	// daily grain is noise, and a viewer who changed it meant to.
	const [bucketChoice, setBucketChoice] = useState<AuditBucket | null>(null);
	const bucket = bucketChoice ?? defaultBucketFor(range);
	// Windows are derived once per range so every query shares one `now`:
	// otherwise each hook's key would carry its own timestamp and nothing
	// would ever hit the cache twice.
	const windows = useMemo(() => auditWindows(range), [range]);

	const eventType = eventTypeFilter === "all" ? undefined : eventTypeFilter;
	const category = categoryFilter === "all" ? undefined : categoryFilter;
	const namespace = namespaceFilter === "all" ? undefined : namespaceFilter;
	const eventTypes = eventTypeSet ?? undefined;

	const { organizationId } = useAuth();

	// The registry's own list of registered types. Named for what it is, so it
	// cannot be confused with the eventTypes FILTER below.
	const { data: registeredTypes } = useAuditEventTypes();
	// The table shows the same window the tiles and the series describe.
	const { data, isLoading } = useAuditLog({
		eventType,
		eventTypes,
		category,
		namespace,
		from: windows.current.from,
		to: windows.current.to,
		pageSize: 100,
	});
	const events = useMemo(() => data?.events ?? [], [data]);
	const exportMutation = useExportAuditLog();

	const scope = { eventType, eventTypes, category, namespace };
	const current = { from: windows.current.from, to: windows.current.to };
	const previous = { from: windows.previous.from, to: windows.previous.to };

	// One headline aggregate per window, grouped by category then event type
	// and NOT filtered by category: every tile slices it client-side (exact,
	// since category is a group dimension), and the security tile reads its
	// own category whatever the viewer is drilling into.
	const headline = {
		eventType,
		eventTypes,
		namespace,
		groupBys: ["category", "event_type"] as AuditGroupDimension[],
	};
	const headlineNow = useAuditAggregate({ ...headline, ...current });
	// The tiles that act as filter controls read this one instead: the window's
	// events with no filter applied at all. A tile is a control, so the number
	// it shows has to be the number its click produces — reading the in-scope
	// aggregate made "Events" display the filtered count while clicking it
	// cleared the filters, and made "New users" drop whichever of its event
	// types fell outside the current category. When nothing is filtered these
	// params hash to the headline's own key, so react-query serves both from
	// one request.
	const unfiltered = {
		groupBys: ["category", "event_type"] as AuditGroupDimension[],
	};
	const totalNow = useAuditAggregate({ ...unfiltered, ...current });
	const totalBefore = useAuditAggregate({ ...unfiltered, ...previous });
	// Distinct actors cannot be read off a grouped count, so they keep their
	// own aggregate; the top-actors list reads the same one.
	const actorsNow = useAuditAggregate({
		...scope,
		...current,
		groupBy: "actor",
	});
	const actorsBefore = useAuditAggregate({
		...scope,
		...previous,
		groupBy: "actor",
	});

	const {
		data: byTimeAndCategory,
		isLoading: seriesLoading,
		error: seriesError,
	} = useAuditAggregate({
		...scope,
		...current,
		groupBys: ["time", "category"],
		bucket,
	});

	const inScopeNow = useMemo(
		() => sliceCategory(headlineNow.data ?? [], category),
		[headlineNow.data, category],
	);
	const byTypeNow = useMemo(() => byEventType(inScopeNow), [inScopeNow]);
	// Counted off the unfiltered aggregate: the new-user event types name the
	// fact directly, so a category drill-down can only subtract from them —
	// never refine them — and must not silently change what the tile means.
	const allTypesNow = useMemo(
		() => byEventType(totalNow.data ?? []),
		[totalNow.data],
	);
	const allTypesBefore = useMemo(
		() => byEventType(totalBefore.data ?? []),
		[totalBefore.data],
	);
	// The registry decides which names still mean "a person joined".
	const newUserTypes = useMemo(
		() => newUserEventTypes(registeredTypes ?? []),
		[registeredTypes],
	);

	// Every clickable tile reads the unfiltered window and selects exactly the
	// filter state that reproduces its own number, so the value and the click
	// can never disagree. "Events" is that window whole; "Security events" is
	// that window sliced to one category, which is why selecting it clears the
	// other two filters rather than intersecting with them.
	const resetFilters = () => {
		setEventTypeFilter("all");
		setCategoryFilter("all");
		setNamespaceFilter("all");
		setEventTypeSet(null);
	};
	const selectSecurityOnly = () => {
		setCategoryFilter(SECURITY_CATEGORY);
		setEventTypeFilter("all");
		setNamespaceFilter("all");
		setEventTypeSet(null);
	};
	// The registry decides which names mean "a person joined", so the tile's
	// filter is that whole set. Selecting it again clears it: a tile that can
	// only be switched on strands the reader on a filter with no obvious way
	// back, since no dropdown shows a set.
	const toggleNewUsersOnly = () => {
		if (showsNewUsersOnly) {
			resetFilters();
			return;
		}
		setEventTypeFilter("all");
		setCategoryFilter("all");
		setNamespaceFilter("all");
		setEventTypeSet(newUserTypes);
	};
	const showsSecurityOnly =
		categoryFilter === SECURITY_CATEGORY &&
		eventTypeFilter === "all" &&
		namespaceFilter === "all" &&
		eventTypeSet === null;
	const showsNewUsersOnly =
		eventTypeSet !== null &&
		eventTypeFilter === "all" &&
		categoryFilter === "all" &&
		namespaceFilter === "all";
	const showsNoFilter =
		eventTypeFilter === "all" &&
		categoryFilter === "all" &&
		namespaceFilter === "all" &&
		eventTypeSet === null;

	const tiles = useMemo(() => {
		const tile = (
			id: string,
			label: string,
			now: number,
			before: number,
			extra: {
				higherIsBetter?: boolean;
				onSelect?: () => void;
				selected?: boolean;
			} = {},
		) => ({
			id,
			label,
			value: now,
			format: "compact" as const,
			delta: relativeChange(now, before),
			deltaLabel: `vs previous ${range}`,
			...extra,
		});
		const newUsers = tile(
			"new-users",
			"New users",
			countEventTypes(allTypesNow, newUserTypes),
			countEventTypes(allTypesBefore, newUserTypes),
			// Clickable through the set form of the event-type filter
			// (QueryAuditLogRequest / AggregateAuditLogRequest event_types).
			// While the wire carried only a scalar event_type there was no way
			// to open this: the tile counts a FAMILY the registry owns and may
			// extend, and selecting one member would have shown a different
			// number from the one that was clicked.
			//
			// With no registered new-user type there is no set to select, so the
			// tile stays presentational rather than offering a click that would
			// filter to nothing.
			newUserTypes.length > 0
				? { onSelect: toggleNewUsersOnly, selected: showsNewUsersOnly }
				: {},
		);
		// Say so when the registry no longer knows the names this tile counts:
		// a zero here would otherwise read as "nobody joined".
		if (registeredTypes !== undefined && newUserTypes.length === 0)
			newUsers.deltaLabel = "no registered new-user event type";
		return [
			tile(
				"events",
				"Events",
				totalCount(totalNow.data ?? []),
				totalCount(totalBefore.data ?? []),
				{ onSelect: resetFilters, selected: showsNoFilter },
			),
			newUsers,
			tile(
				"actors",
				"Active actors",
				distinctCount(actorsNow.data ?? []),
				distinctCount(actorsBefore.data ?? []),
				// Not clickable: this counts distinct actors, a cardinality with
				// no corresponding single filter value — there is no "actor
				// X" the tile could select the way a category or event type
				// names one.
			),
			// A rise in security events is worth a look, not a celebration.
			tile(
				"security",
				"Security events",
				totalCount(sliceCategory(totalNow.data ?? [], SECURITY_CATEGORY)),
				totalCount(sliceCategory(totalBefore.data ?? [], SECURITY_CATEGORY)),
				{
					higherIsBetter: false,
					// category is an exact, already-existing filter value, so this
					// reuses the same Select state the "Category" dropdown drives.
					onSelect: selectSecurityOnly,
					selected: showsSecurityOnly,
				},
			),
		];
	}, [
		range,
		registeredTypes,
		newUserTypes,
		allTypesNow,
		allTypesBefore,
		totalNow.data,
		totalBefore.data,
		actorsNow.data,
		actorsBefore.data,
		showsNoFilter,
		showsSecurityOnly,
	]);
	const tilesLoading = totalNow.isLoading || actorsNow.isLoading;
	const tilesError = totalNow.error ?? actorsNow.error;

	const { directory: actorNames, failed: actorNamesFailed } =
		usePrincipalDirectory(organizationId ?? "", [
			...events.map((e) => e.actorId),
			...(actorsNow.data ?? []).map((b) => b.key),
		]);
	const stacked = useMemo(
		() => pivotByTime(byTimeAndCategory ?? []),
		[byTimeAndCategory],
	);
	const topTypes = useMemo(() => topGroups(byTypeNow, 6), [byTypeNow]);
	const topActors = useMemo(
		() => topGroups(actorsNow.data ?? [], 6),
		[actorsNow.data],
	);

	// Categories are the distinct set advertised by the registry.
	const categories = useMemo(() => {
		const set = new Set((registeredTypes ?? []).map((t) => t.category));
		return Array.from(set).sort();
	}, [registeredTypes]);

	// Namespaces are the modules that mint events into this tenant's audit spine.
	// One today; a composed workspace adds one per module that emits.
	const namespaces = useMemo(() => {
		const set = new Set(
			(registeredTypes ?? []).map((t) => t.namespace).filter(Boolean),
		);
		return Array.from(set).sort();
	}, [registeredTypes]);

	const visibleEventTypes = useMemo(() => {
		let list = registeredTypes ?? [];
		if (category) list = list.filter((t) => t.category === category);
		if (namespace) list = list.filter((t) => t.namespace === namespace);
		return list.slice().sort((a, b) => a.name.localeCompare(b.name));
	}, [registeredTypes, category, namespace]);

	const handleExport = (format: "csv" | "json") => {
		// The download is what the page is showing: same filters, same window.
		exportMutation.mutate({
			format,
			eventType,
			eventTypes,
			category,
			namespace,
			from: windows.current.from,
			to: windows.current.to,
		});
	};

	const actions = (
		<>
			<SegmentedControl
				size="sm"
				aria-label="Time range"
				options={AUDIT_RANGE_PRESETS.map((preset) => ({
					value: preset,
					label: preset,
				}))}
				value={range}
				onValueChange={(value) => {
					setRange(value);
					setBucketChoice(null);
				}}
			/>
			<SegmentedControl
				size="sm"
				aria-label="Time bucket"
				options={AUDIT_BUCKETS.map((b) => ({ value: b, label: b }))}
				value={bucket}
				onValueChange={setBucketChoice}
			/>
			<DropdownMenu>
				<DropdownMenuTrigger
					render={
						<Button
							variant="outline"
							size="sm"
							disabled={exportMutation.isPending}
						>
							<Download className="mr-2 h-4 w-4" />
							{exportMutation.isPending ? "Exporting..." : "Export"}
						</Button>
					}
				/>
				<DropdownMenuContent align="end">
					<DropdownMenuItem onClick={() => handleExport("csv")}>
						Export as CSV
					</DropdownMenuItem>
					<DropdownMenuItem onClick={() => handleExport("json")}>
						Export as JSON
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
			<Select
				value={categoryFilter}
				items={[
					{ value: "all", label: "All categories" },
					...categories.map((value) => ({
						value,
						label: formatAuditAction(value),
					})),
				]}
				onValueChange={(v) => {
					if (v) {
						setCategoryFilter(v);
						setEventTypeFilter("all");
						// No dropdown can show a set, so a set left on here would
						// narrow the result behind the reader's back.
						setEventTypeSet(null);
					}
				}}
			>
				<SelectTrigger className="w-[160px]">
					<SelectValue placeholder="Category" />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="all">All categories</SelectItem>
					{categories.map((c) => (
						<SelectItem key={c} value={c}>
							{formatAuditAction(c)}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Select
				value={namespaceFilter}
				items={[
					{ value: "all", label: "All namespaces" },
					...namespaces.map((n) => ({ value: n, label: n })),
				]}
				onValueChange={(v) => {
					if (v) {
						setNamespaceFilter(v);
						setEventTypeFilter("all");
						setEventTypeSet(null);
					}
				}}
			>
				<SelectTrigger className="w-[160px]">
					<SelectValue placeholder="Namespace" />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="all">All namespaces</SelectItem>
					{namespaces.map((n) => (
						<SelectItem key={n} value={n}>
							{n}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Select
				value={eventTypeFilter}
				items={[
					{ value: "all", label: "All event types" },
					...visibleEventTypes.map((t) => ({
						value: t.name,
						label: formatAuditAction(t.name),
					})),
				]}
				onValueChange={(v) => {
					if (v) {
						setEventTypeFilter(v);
						setEventTypeSet(null);
					}
				}}
			>
				<SelectTrigger className="w-[220px]">
					<SelectValue placeholder="Filter by event type" />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="all">All event types</SelectItem>
					{visibleEventTypes.map((t) => (
						<SelectItem key={t.name} value={t.name}>
							{formatAuditAction(t.name)}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</>
	);

	const dashboard: DashboardData = {
		title: "Audit Log",
		// When a set filter is on, say so here. Every dropdown still reads "all"
		// — none of them can express a set — so without this the table, the
		// series and the breakdowns would all be narrowed with nothing on the
		// page naming the narrowing or how to undo it.
		description: showsNewUsersOnly
			? `New users in the last ${range}: ${newUserTypes.length} event ${
					newUserTypes.length === 1 ? "type" : "types"
				} the registry marks as a person joining. Select the tile again to clear it.`
			: `What happened in the last ${range}, and who did it.`,
		actions,
		widgets: [
			{
				id: "headline",
				kind: "metrics",
				metrics: tiles,
				isLoading: tilesLoading,
				error: tilesError,
				emptyMessage: "No events in range.",
			},
			{
				id: "events-by-category",
				kind: "series",
				span: "full",
				title: "Events over time, by category",
				description: `Per ${bucket}, stacked; the outline is the total.`,
				series: stacked.series,
				isLoading: seriesLoading,
				error: seriesError,
				emptyMessage: "No events in range.",
			},
			{
				id: "top-event-types",
				kind: "bars",
				title: "Top event types",
				description: "Most frequent events for the current filter.",
				items: topTypes.map((b) => ({
					label: formatAuditAction(b.key),
					value: b.count,
				})),
				isLoading: headlineNow.isLoading,
				error: headlineNow.error,
				emptyMessage: "No events in range.",
			},
			{
				id: "top-actors",
				kind: "bars",
				title: "Top actors",
				description: "Who did the most, for the current filter.",
				items: topActors.map((b) => ({
					label: actorNames.get(b.key) ?? b.key.slice(0, 8),
					value: b.count,
				})),
				isLoading: actorsNow.isLoading,
				error: actorsNow.error,
				emptyMessage: "No events in range.",
			},
			{
				id: "audit-table",
				kind: "node",
				span: "full",
				node: (
					<div className="space-y-2">
						{actorNamesFailed ? (
							<p className="text-xs text-muted-foreground">
								Actor names could not be loaded; actors are shown by id.
							</p>
						) : null}
						<AuditTable
							events={events}
							isLoading={isLoading}
							actorNames={actorNames}
						/>
					</div>
				),
			},
		],
	};

	return <Dashboard data={dashboard} />;
}
