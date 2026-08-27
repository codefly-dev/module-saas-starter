"use client";

import { Download } from "lucide-react";
import { useMemo, useState } from "react";
import { Dashboard, type DashboardData } from "@/components/dashboard";
import {
	Button,
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { formatAuditAction } from "../model/transforms";
import { useExportAuditLog } from "../service/mutations";
import {
	useAuditAggregate,
	useAuditEventTypes,
	useAuditLog,
} from "../service/queries";
import { AuditTable } from "./audit-table";

export function AuditPage() {
	const [eventTypeFilter, setEventTypeFilter] = useState("all");
	const [categoryFilter, setCategoryFilter] = useState("all");

	const eventType = eventTypeFilter === "all" ? undefined : eventTypeFilter;
	const category = categoryFilter === "all" ? undefined : categoryFilter;

	const { data: eventTypes } = useAuditEventTypes();
	const { data, isLoading } = useAuditLog({ eventType, category, pageSize: 100 });
	const exportMutation = useExportAuditLog();

	const {
		data: byType,
		isLoading: byTypeLoading,
		error: byTypeError,
	} = useAuditAggregate({ eventType, category, groupBy: "event_type" });
	const {
		data: byDay,
		isLoading: byDayLoading,
		error: byDayError,
	} = useAuditAggregate({
		eventType,
		category,
		groupBy: "time",
		bucket: "day",
	});

	// Categories are the distinct set advertised by the registry.
	const categories = useMemo(() => {
		const set = new Set((eventTypes ?? []).map((t) => t.category));
		return Array.from(set).sort();
	}, [eventTypes]);

	const visibleEventTypes = useMemo(() => {
		const list = eventTypes ?? [];
		return (category ? list.filter((t) => t.category === category) : list)
			.slice()
			.sort((a, b) => a.name.localeCompare(b.name));
	}, [eventTypes, category]);

	const topTypes = useMemo(
		() => (byType ?? []).slice(0, 6),
		[byType],
	);
	const dayPoints = useMemo(
		() =>
			(byDay ?? [])
				.slice()
				.sort((a, b) => a.key.localeCompare(b.key))
				.map((b) => b.count),
		[byDay],
	);

	const handleExport = (format: "csv" | "json") => {
		exportMutation.mutate({ format, eventType });
	};

	const actions = (
		<>
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
		description: "View all system events and user activity.",
		actions,
		widgets: [
			{
				id: "events-over-time",
				kind: "sparkline",
				title: "Events over time",
				description: "Daily event volume for the current filter.",
				points: dayPoints,
				isLoading: byDayLoading,
				error: byDayError,
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
				isLoading: byTypeLoading,
				error: byTypeError,
				emptyMessage: "No events in range.",
			},
			{
				id: "audit-table",
				kind: "node",
				span: "full",
				node: <AuditTable events={data?.events ?? []} isLoading={isLoading} />,
			},
		],
	};

	return <Dashboard data={dashboard} />;
}
