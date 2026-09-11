"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { useMemo } from "react";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import { Badge } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import {
	formatActorType,
	formatAuditAction,
	resolveActorName,
} from "../model/transforms";
import type { AuditEvent, PrincipalDirectory } from "../model/types";

const col = createColumnHelper<AuditEvent>();

export function AuditTable({
	events,
	isLoading,
	actorNames,
}: {
	events: AuditEvent[];
	isLoading: boolean;
	actorNames: PrincipalDirectory;
}) {
	const columns = useMemo(
		() => [
			col.accessor("createdAt", {
				header: "Time",
				cell: (info) => (
					<span className="whitespace-nowrap text-muted-foreground">
						{formatDate(info.getValue())}
					</span>
				),
			}),
			col.accessor("eventType", {
				header: "Event Type",
				cell: (info) => {
					const event = info.row.original;
					return (
						<div className="flex flex-col gap-1">
							<Badge variant="outline">
								{formatAuditAction(info.getValue())}
							</Badge>
							{event.category ? (
								<span className="text-[10px] uppercase tracking-wide text-muted-foreground">
									{event.category}
								</span>
							) : null}
						</div>
					);
				},
			}),
			col.accessor("actorId", {
				header: "Actor",
				cell: (info) => {
					const actorId = info.getValue();
					const resolved = actorNames.has(actorId);
					return (
						<div className="flex flex-col gap-1">
							<span
								className={
									resolved
										? "text-foreground"
										: "font-mono text-xs text-muted-foreground"
								}
							>
								{resolveActorName(actorId, actorNames)}
							</span>
							{info.row.original.actorType ? (
								<span className="text-[10px] uppercase tracking-wide text-muted-foreground">
									{formatActorType(info.row.original.actorType)}
								</span>
							) : null}
						</div>
					);
				},
			}),
			col.accessor("resource", {
				header: "Resource",
				cell: (info) => {
					const event = info.row.original;
					return (
						<span className="text-muted-foreground">
							{event.resource}/{truncateUUID(event.resourceId)}
						</span>
					);
				},
			}),
			col.accessor("ipAddress", {
				header: "IP Address",
				cell: (info) => (
					<span className="font-mono text-xs text-muted-foreground">
						{info.getValue() || "-"}
					</span>
				),
			}),
			col.display({
				id: "payload",
				header: "Payload",
				cell: (info) => {
					const { payload } = info.row.original;
					if (!payload || Object.keys(payload).length === 0) {
						return <span className="text-muted-foreground">-</span>;
					}
					return (
						<code className="block max-w-[280px] truncate font-mono text-xs text-muted-foreground">
							{Object.entries(payload)
								.map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`)
								.join(" ")}
						</code>
					);
				},
			}),
		],
		[actorNames],
	);

	const table = useReactTable({
		data: events,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});

	return (
		<DataTable
			table={table}
			isLoading={isLoading}
			emptyMessage="No audit events"
		/>
	);
}
