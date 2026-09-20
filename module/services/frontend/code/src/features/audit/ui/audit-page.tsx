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
	defaultBucketFor,
	distinctCount,
	NEW_USER_EVENT_TYPES,
	newUserCount,
	pivotByTime,
	relativeChange,
	SECURITY_CATEGORY,
	topGroups,
	totalCount,
} from "../model/analytics";
import { formatAuditAction } from "../model/transforms";
import { useExportAuditLog } from "../service/mutations";
import {
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

	const { organizationId } = useAuth();

	const { data: eventTypes } = useAuditEventTypes();
	const { data, isLoading } = useAuditLog({
		eventType,
		category,
		namespace,
		pageSize: 100,
	});
	const events = useMemo(() => data?.events ?? [], [data]);
	const exportMutation = useExportAuditLog();

	const scope = { eventType, category, namespace };
	const current = { from: windows.current.from, to: windows.current.to };
	const previous = { from: windows.previous.from, to: windows.previous.to };

	// Headline: total events now and in the equal window before, for a delta.
	const eventsNow = useAuditAggregate({
		...scope,
		...current,
		groupBy: "category",
	});
	const eventsBefore = useAuditAggregate({
		...scope,
		...previous,
		groupBy: "category",
	});
	// Distinct actors seen in the window.
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
	// Security-category events. Not filtered by the page's category control:
	// the tile answers "how much security activity" regardless of what the
	// viewer is drilling into, and reads zero only when there is none.
	const securityNow = useAuditAggregate({
		...current,
		eventType,
		namespace,
		category: SECURITY_CATEGORY,
		groupBy: "event_type",
	});
	const securityBefore = useAuditAggregate({
		...previous,
		eventType,
		namespace,
		category: SECURITY_CATEGORY,
		groupBy: "event_type",
	});
	// New users: the registered event types that mean a person joined, counted
	// from a by-type aggregate so a renamed type shows as an empty tile rather
	// than a wrong number.
	const byTypeNow = useAuditAggregate({
		...scope,
		...current,
		groupBy: "event_type",
	});
	const byTypeBefore = useAuditAggregate({
		...scope,
		...previous,
		groupBy: "event_type",
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

	const tiles = useMemo(() => {
		const tile = (
			id: string,
			label: string,
			now: number,
			before: number,
			extra: { higherIsBetter?: boolean } = {},
		) => ({
			id,
			label,
			value: now,
			format: "compact" as const,
			delta: relativeChange(now, before),
			deltaLabel: `vs previous ${range}`,
			...extra,
		});
		return [
			tile(
				"events",
				"Events",
				totalCount(eventsNow.data ?? []),
				totalCount(eventsBefore.data ?? []),
			),
			tile(
				"new-users",
				"New users",
				newUserCount(byTypeNow.data ?? []),
				newUserCount(byTypeBefore.data ?? []),
			),
			tile(
				"actors",
				"Active actors",
				distinctCount(actorsNow.data ?? []),
				distinctCount(actorsBefore.data ?? []),
			),
			// A rise in security events is worth a look, not a celebration.
			tile(
				"security",
				"Security events",
				totalCount(securityNow.data ?? []),
				totalCount(securityBefore.data ?? []),
				{
					higherIsBetter: false,
				},
			),
		];
	}, [
		range,
		eventsNow.data,
		eventsBefore.data,
		byTypeNow.data,
		byTypeBefore.data,
		actorsNow.data,
		actorsBefore.data,
		securityNow.data,
		securityBefore.data,
	]);
	const tilesLoading =
		eventsNow.isLoading ||
		byTypeNow.isLoading ||
		actorsNow.isLoading ||
		securityNow.isLoading;
	const tilesError =
		eventsNow.error ?? byTypeNow.error ?? actorsNow.error ?? securityNow.error;

	const { directory: actorNames, failed: actorNamesFailed } =
		usePrincipalDirectory(organizationId ?? "", [
			...events.map((e) => e.actorId),
			...(actorsNow.data ?? []).map((b) => b.key),
		]);
	const stacked = useMemo(
		() => pivotByTime(byTimeAndCategory ?? []),
		[byTimeAndCategory],
	);
	const topTypes = useMemo(
		() => topGroups(byTypeNow.data ?? [], 6),
		[byTypeNow.data],
	);
	const topActors = useMemo(
		() => topGroups(actorsNow.data ?? [], 6),
		[actorsNow.data],
	);

	// Categories are the distinct set advertised by the registry.
	const categories = useMemo(() => {
		const set = new Set((eventTypes ?? []).map((t) => t.category));
		return Array.from(set).sort();
	}, [eventTypes]);

	// Namespaces are the modules that mint events into this tenant's audit spine.
	// One today; a composed workspace adds one per module that emits.
	const namespaces = useMemo(() => {
		const set = new Set(
			(eventTypes ?? []).map((t) => t.namespace).filter(Boolean),
		);
		return Array.from(set).sort();
	}, [eventTypes]);

	const visibleEventTypes = useMemo(() => {
		let list = eventTypes ?? [];
		if (category) list = list.filter((t) => t.category === category);
		if (namespace) list = list.filter((t) => t.namespace === namespace);
		return list.slice().sort((a, b) => a.name.localeCompare(b.name));
	}, [eventTypes, category, namespace]);

	const handleExport = (format: "csv" | "json") => {
		exportMutation.mutate({ format, eventType });
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
				onValueChange={(v) => {
					if (v) {
						setCategoryFilter(v);
						setEventTypeFilter("all");
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
							{c}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
			<Select
				value={namespaceFilter}
				onValueChange={(v) => {
					if (v) {
						setNamespaceFilter(v);
						setEventTypeFilter("all");
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
				onValueChange={(v) => {
					if (v) setEventTypeFilter(v);
				}}
			>
				<SelectTrigger className="w-[220px]">
					<SelectValue placeholder="Filter by event type" />
				</SelectTrigger>
				<SelectContent>
					<SelectItem value="all">All event types</SelectItem>
					{visibleEventTypes.map((t) => (
						<SelectItem key={t.name} value={t.name}>
							{t.name}
						</SelectItem>
					))}
				</SelectContent>
			</Select>
		</>
	);

	const dashboard: DashboardData = {
		title: "Audit Log",
		description: `What happened in the last ${range}, and who did it.`,
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
				isLoading: byTypeNow.isLoading,
				error: byTypeNow.error,
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
