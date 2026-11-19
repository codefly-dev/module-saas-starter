"use client";

import {
	createColumnHelper,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Radio } from "lucide-react";
import type { EventSubscriptionSummary } from "@/gen/saas/events/v1/operations_pb";
import { readOutcome, staleReadNotice } from "@/shared/lib/read-outcome";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import { Banner, EmptyState, Stack } from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { deliveryOrderLabel } from "../model/event-labels";
import { ReadFailure } from "./read-failure";

const col = createColumnHelper<EventSubscriptionSummary>();

const columns = [
	col.accessor("subscriberPrincipalId", {
		header: "Subscriber",
		cell: (info) => (
			<span title={info.getValue()}>{truncateUUID(info.getValue())}</span>
		),
	}),
	col.accessor("typePattern", {
		header: "Events it receives",
		cell: (info) => <span className="font-medium">{info.getValue()}</span>,
	}),
	col.accessor("queue", { header: "Queue" }),
	col.accessor("delivery", {
		header: "Delivery order",
		cell: (info) => deliveryOrderLabel(info.getValue()),
	}),
	col.accessor("queueDeadLetter", {
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
	col.accessor("createdAt", {
		header: "Created",
		cell: (info) => (
			<span className="text-muted-foreground">
				{formatDate(info.getValue())}
			</span>
		),
	}),
];

// See event-types-table.tsx: never a fresh `[]` per render.
const NO_SUBSCRIPTIONS: EventSubscriptionSummary[] = [];

export function SubscriptionsTable({
	subscriptions,
	isLoading,
	isError,
	error,
	onRetry,
}: {
	/** Absent while there is no answer. */
	subscriptions: EventSubscriptionSummary[] | undefined;
	isLoading: boolean;
	isError: boolean;
	error: unknown;
	onRetry: () => void;
}) {
	const table = useReactTable({
		data: subscriptions ?? NO_SUBSCRIPTIONS,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getRowId: (row) => row.id,
	});

	const outcome = readOutcome(isError, error);
	if (isError && (outcome === "forbidden" || subscriptions === undefined)) {
		return (
			<ReadFailure
				subject="the subscriptions"
				error={error}
				onRetry={onRetry}
			/>
		);
	}
	if (!isLoading && subscriptions?.length === 0) {
		return (
			<EmptyState
				icon={<Radio />}
				heading="No active subscriptions"
				description="A subscription sends matching events to one subscriber. Modules and integrations create them when they start listening for events."
			/>
		);
	}
	const stale = staleReadNotice(outcome, "the subscriptions");
	return (
		<Stack gap={3}>
			{stale && <Banner tone="warning">{stale}</Banner>}
			<DataTable
				table={table}
				isLoading={isLoading}
				emptyMessage="No active subscriptions"
			/>
		</Stack>
	);
}
