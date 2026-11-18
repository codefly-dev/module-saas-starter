"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Workflow } from "lucide-react";
import type { EventTypeSnapshot } from "@/gen/saas/events/v1/operations_pb";
import { readOutcome, staleReadNotice } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import { Badge, Banner, EmptyState, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import {
	eventVisibilityLabel,
	eventVisibilityTone,
} from "../model/event-labels";
import { ReadFailure } from "./read-failure";

const col = createColumnHelper<EventTypeSnapshot>();

const columns = [
	col.accessor("type", {
		header: "Event type",
		cell: (info) => <span className="font-medium">{info.getValue()}</span>,
	}),
	col.accessor("visibility", {
		header: "Visibility",
		cell: (info) => (
			<Badge tone={eventVisibilityTone(info.getValue())}>
				{eventVisibilityLabel(info.getValue())}
			</Badge>
		),
	}),
	col.accessor("publisher", {
		header: "Published by",
		cell: (info) =>
			info.getValue() || (
				<span className="text-muted-foreground">Not declared</span>
			),
	}),
	col.accessor("major", {
		header: "Version",
		cell: (info) =>
			info.getValue() || (
				<span className="text-muted-foreground">Not declared</span>
			),
	}),
	col.accessor("subscribers", {
		header: "Subscribers",
		cell: (info) => info.getValue().toLocaleString(),
	}),
	col.accessor("totalEvents", {
		header: "Events recorded",
		cell: (info) => info.getValue().toLocaleString(),
	}),
	col.accessor("unpublished", {
		header: "Waiting to be sent",
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
	col.accessor("lastEventAt", {
		header: "Last event",
		cell: (info) => (
			<span className="text-muted-foreground">
				{info.getValue() ? formatDate(info.getValue()) : "None yet"}
			</span>
		),
	}),
];

// The rows when there is no answer: TanStack Table resets on every new `data`
// array, so a fresh `[]` per render would re-render the page forever.
const NO_EVENT_TYPES: EventTypeSnapshot[] = [];

export function EventTypesTable({
	eventTypes,
	isLoading,
	isError,
	error,
	onRetry,
}: {
	/** Absent while there is no answer. */
	eventTypes: EventTypeSnapshot[] | undefined;
	isLoading: boolean;
	isError: boolean;
	error: unknown;
	onRetry: () => void;
}) {
	const table = useReactTable({
		data: eventTypes ?? NO_EVENT_TYPES,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.type,
	});

	const outcome = readOutcome(isError, error);
	// A refusal takes the rows with it; a transient failure keeps the last good
	// answer on screen and says it may be out of date.
	if (isError && (outcome === "forbidden" || eventTypes === undefined)) {
		return (
			<ReadFailure subject="the event types" error={error} onRetry={onRetry} />
		);
	}
	if (!isLoading && eventTypes?.length === 0) {
		return (
			<EmptyState
				icon={<Workflow />}
				heading="No event types yet"
				description="An event type is a kind of thing the platform announces, such as an organization being created. Each appears here once it is declared or first published."
			/>
		);
	}
	const stale = staleReadNotice(outcome, "the event types");
	return (
		<Stack gap={3}>
			{stale && <Banner tone="warning">{stale}</Banner>}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No event types yet"
			/>
		</Stack>
	);
}
