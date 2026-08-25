"use client";

import { Download } from "lucide-react";
import { useMemo, useState } from "react";
import { Sparkline } from "@/components/sparkline";
import {
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
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

	const { data: byType } = useAuditAggregate({ eventType, category, groupBy: "event_type" });
	const { data: byDay } = useAuditAggregate({
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
	const maxTypeCount = topTypes.reduce((m, b) => Math.max(m, b.count), 0) || 1;
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

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Audit Log</h1>
					<p className="text-muted-foreground">
						View all system events and user activity.
					</p>
				</div>
				<div className="flex items-center gap-2">
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
				</div>
			</div>

			<div className="grid gap-4 md:grid-cols-2">
				<Card>
					<CardHeader>
						<CardTitle className="text-base">Events over time</CardTitle>
						<CardDescription>Daily event volume for the current filter.</CardDescription>
					</CardHeader>
					<CardContent>
						{dayPoints.length > 0 ? (
							<Sparkline points={dayPoints} className="text-primary/70" />
						) : (
							<p className="text-sm text-muted-foreground">No events in range.</p>
						)}
					</CardContent>
				</Card>
				<Card>
					<CardHeader>
						<CardTitle className="text-base">Top event types</CardTitle>
						<CardDescription>Most frequent events for the current filter.</CardDescription>
					</CardHeader>
					<CardContent className="space-y-2">
						{topTypes.length > 0 ? (
							topTypes.map((b) => (
								<div key={b.key} className="space-y-1">
									<div className="flex items-center justify-between text-xs">
										<span className="text-muted-foreground">
											{formatAuditAction(b.key)}
										</span>
										<span className="font-mono">{b.count}</span>
									</div>
									<div className="h-2 rounded-full bg-muted">
										<div
											className="h-2 rounded-full bg-primary/70"
											style={{ width: `${(b.count / maxTypeCount) * 100}%` }}
										/>
									</div>
								</div>
							))
						) : (
							<p className="text-sm text-muted-foreground">No events in range.</p>
						)}
					</CardContent>
				</Card>
			</div>

			<AuditTable
				events={data?.events ?? []}
				isLoading={isLoading}
			/>
		</div>
	);
}
