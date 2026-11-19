"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Layers } from "lucide-react";
import type { JobQueueSnapshot } from "@/gen/saas/jobs/v1/jobs_pb";
import { readOutcome, staleReadNotice } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import { Banner, Button, EmptyState, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { ReadFailure } from "./read-failure";

const col = createColumnHelper<JobQueueSnapshot>();

function count(value: bigint) {
	return value.toLocaleString();
}

function alarming(value: bigint) {
	return (
		<span className={value !== BigInt(0) ? "text-destructive" : undefined}>
			{count(value)}
		</span>
	);
}

const columns = [
	col.accessor("queue", {
		header: "Queue",
		cell: (info) => <span className="font-medium">{info.getValue()}</span>,
	}),
	col.accessor("ready", {
		header: "Waiting to start",
		cell: (info) => count(info.getValue()),
	}),
	col.accessor("processing", {
		header: "Running now",
		cell: (info) => count(info.getValue()),
	}),
	col.accessor("scheduled", {
		header: "Scheduled for later",
		cell: (info) => count(info.getValue()),
	}),
	col.accessor("deadLetter", {
		header: "Failed for good",
		cell: (info) => alarming(info.getValue()),
	}),
	col.accessor("expiredLeases", {
		header: "Stalled mid-run",
		cell: (info) => alarming(info.getValue()),
	}),
	col.accessor("oldestReadyAt", {
		header: "Oldest waiting since",
		cell: (info) => (
			<span className="text-muted-foreground">
				{info.getValue() ? formatDate(info.getValue()) : "Nothing waiting"}
			</span>
		),
	}),
];

// The rows when there is no answer: TanStack Table resets on every new `data`
// array, so a fresh `[]` per render would re-render the page forever.
const NO_QUEUES: JobQueueSnapshot[] = [];

export function QueuesTable({
	queues,
	isLoading,
	isError,
	error,
	onRetry,
	queueFilter,
	onClearFilter,
}: {
	/** Absent while there is no answer. */
	queues: JobQueueSnapshot[] | undefined;
	isLoading: boolean;
	isError: boolean;
	error: unknown;
	onRetry: () => void;
	/** The queue name the page is narrowed to, or "" for every queue. */
	queueFilter: string;
	onClearFilter: () => void;
}) {
	const table = useReactTable({
		data: queues ?? NO_QUEUES,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.queue,
	});

	const outcome = readOutcome(isError, error);
	// A refusal takes the rows with it; a transient failure keeps the last good
	// answer on screen and says it may be out of date.
	if (isError && (outcome === "forbidden" || queues === undefined)) {
		return <ReadFailure subject="the queues" error={error} onRetry={onRetry} />;
	}
	if (!isLoading && queues?.length === 0) {
		return queueFilter ? (
			<EmptyState
				icon={<Layers />}
				heading={`No queue is named “${queueFilter}”`}
				description="Queue names must match exactly. Clear the filter to see every queue."
			>
				<Button variant="outline" onClick={onClearFilter}>
					Clear filter
				</Button>
			</EmptyState>
		) : (
			<EmptyState
				icon={<Layers />}
				heading="No queues yet"
				description="A queue is a line of background work waiting its turn. One appears here the first time the platform has work for it."
			/>
		);
	}
	const stale = staleReadNotice(outcome, "the queues");
	return (
		<Stack gap={3}>
			{stale && <Banner tone="warning">{stale}</Banner>}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No queues yet"
			/>
		</Stack>
	);
}
