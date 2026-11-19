"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import type { QueueDeadLetters } from "@/gen/saas/events/v1/operations_pb";
import { DataTable } from "@/shared/ui/data-table";

const col = createColumnHelper<QueueDeadLetters>();

const columns = [
	col.accessor("queue", {
		header: "Queue",
		cell: (info) => <span className="font-medium">{info.getValue()}</span>,
	}),
	col.accessor("deadLetter", {
		header: "Undelivered",
		cell: (info) => (
			<span
				className={
					info.getValue() !== BigInt(0) ? "text-destructive" : undefined
				}
			>
				{info.getValue().toLocaleString()}
			</span>
		),
	}),
];

/**
 * Events set aside after every delivery attempt failed, per subscriber queue.
 * The page mounts this only when the read answered with at least one queue.
 */
export function UndeliveredTable({ queues }: { queues: QueueDeadLetters[] }) {
	const table = useReactTable({
		data: queues,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.queue,
	});
	return <DataTable table={table} emptyMessage="Nothing undelivered" />;
}
