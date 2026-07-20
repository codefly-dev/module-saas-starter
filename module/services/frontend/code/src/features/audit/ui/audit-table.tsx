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
import { formatAuditAction } from "../model/transforms";
import type { AuditEvent } from "../model/types";

const col = createColumnHelper<AuditEvent>();

export function AuditTable({
	events,
	isLoading,
}: {
	events: AuditEvent[];
	isLoading: boolean;
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
			col.accessor("action", {
				header: "Action",
				cell: (info) => (
					<Badge variant="outline">{formatAuditAction(info.getValue())}</Badge>
				),
			}),
			col.accessor("actorId", {
				header: "Actor",
				cell: (info) => (
					<span className="font-mono text-xs text-muted-foreground">
						{truncateUUID(info.getValue())}
					</span>
				),
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
		],
		[],
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
