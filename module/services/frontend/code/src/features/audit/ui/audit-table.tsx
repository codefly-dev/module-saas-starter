"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { JsonView } from "@codefly-dev/ui/content";
import { useMemo } from "react";
import { ResourceLabel } from "@/components/resource-label";
import { formatDate } from "@/shared/lib/utils";
import { Badge } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import {
	formatActorType,
	formatAuditAction,
	resolveActor,
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
				// The column renders a name but accesses an id, so the default
				// sort would order rows by raw uuid — visibly arbitrary against
				// the names on screen. Sort by what the cell actually shows.
				sortingFn: (a, b) =>
					resolveActor(a.original.actorId, actorNames).label.localeCompare(
						resolveActor(b.original.actorId, actorNames).label,
					),
				cell: (info) => {
					const actor = resolveActor(info.getValue(), actorNames);
					return (
						<div className="flex flex-col gap-1">
							<span
								className={
									actor.resolved
										? "text-foreground"
										: "font-mono text-xs text-muted-foreground"
								}
							>
								{actor.label}
							</span>
							{info.row.original.actorType ? (
								<span className="text-[10px] uppercase tracking-wide text-muted-foreground">
									{formatActorType(info.row.original.actorType)}
								</span>
							) : null}
							{info.row.original.clientId ? (
								<span className="text-[10px] text-muted-foreground">
									via{" "}
									<span className="font-mono">
										{info.row.original.clientId}
									</span>
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
							<ResourceLabel
								resource={event.resource}
								id={event.resourceId}
								orgId={event.orgId}
							/>
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
				header: "Details",
				cell: (info) => {
					const { payload, id, actorId, resourceId } = info.row.original;
					return (
						<details>
							<summary className="cursor-pointer text-sm">
								Technical details
							</summary>
							<JsonView
								value={{ eventId: id, actorId, resourceId, payload }}
								label="Technical details"
								className="mt-1 max-h-80 max-w-sm overflow-auto"
							/>
						</details>
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
