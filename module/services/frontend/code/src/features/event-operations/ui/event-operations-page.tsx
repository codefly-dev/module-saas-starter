"use client";

import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { EmptyState } from "@codefly-dev/ui/layout";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { Radio, RefreshCw, Workflow } from "lucide-react";
import { useMemo } from "react";
import type {
	EventSubscriptionSummary,
	EventTypeSnapshot,
	QueueDeadLetters,
} from "@/gen/saas/events/v1/operations_pb";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Skeleton,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { useEventOperations, useEventSubscriptions } from "../service/queries";

// Catalog visibility is a free-form string carried from the declared event
// catalog (internal | tenant | external). Undeclared runtime types arrive with
// an empty visibility.
export function eventVisibilityLabel(visibility: string): string {
	return (
		{
			internal: "Internal",
			tenant: "Tenant",
			external: "External",
			"": "Undeclared",
		}[visibility] ?? visibility
	);
}

function visibilityBadgeVariant(visibility: string) {
	if (visibility === "external") return "default" as const;
	if (visibility === "tenant") return "secondary" as const;
	if (visibility === "") return "destructive" as const;
	return "outline" as const;
}

function optionalDate(value?: Timestamp): string {
	return formatDate(value ? timestampDate(value).toISOString() : undefined);
}

// What a person can do about a read that did not come back: nothing but ask,
// when it was refused; otherwise the Refresh button is right there.
function loadFailureDetail(error: unknown): string {
	return readOutcome(true, error) === "forbidden"
		? "You don’t have access to this page. Ask a platform administrator to grant it."
		: "Use Refresh to try again. If it keeps failing, tell your administrator.";
}

export function EventOperationsPage() {
	const operations = useEventOperations();
	const subscriptions = useEventSubscriptions();

	const relay = operations.isError ? undefined : operations.data?.relay;
	const eventTypes = useMemo(
		() => operations.data?.eventTypes ?? [],
		[operations.data?.eventTypes],
	);
	const deadLetters = useMemo(
		() => operations.data?.deadLetters ?? [],
		[operations.data?.deadLetters],
	);

	const backlog = relay?.backlog ?? BigInt(0);

	return (
		<PageBody>
			<PageHeader
				title="Event operations"
				description="The events the platform publishes, who subscribes to them, how far delivery is behind, and what could not be delivered. Event contents are never shown here."
				actions={
					<Button
						variant="outline"
						onClick={() =>
							Promise.all([operations.refetch(), subscriptions.refetch()])
						}
						disabled={operations.isFetching || subscriptions.isFetching}
					>
						<RefreshCw className="h-4 w-4" />
						Refresh
					</Button>
				}
			/>

			<div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
				<MetricCard
					title="Total events"
					value={relay ? relay.totalEvents.toLocaleString() : "—"}
					description="Recorded for delivery"
				/>
				<MetricCard
					title="Published"
					value={relay ? relay.publishedEvents.toLocaleString() : "—"}
					description="Sent on to subscribers"
				/>
				<MetricCard
					title="Backlog"
					value={relay ? backlog.toLocaleString() : "—"}
					description="Waiting to be sent"
					warning={backlog !== BigInt(0)}
				/>
				<MetricCard
					title="Oldest waiting event"
					value={optionalDate(relay?.oldestUnpublishedAt)}
					description="When the oldest unsent event was recorded"
					warning={Boolean(relay?.oldestUnpublishedAt)}
				/>
			</div>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Event types
					</CardTitle>
					<CardDescription>
						Every declared event type with its counts. Types that were published
						without being declared are listed too.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{operations.isLoading ? (
						<Skeleton className="h-48 w-full" />
					) : operations.isError ? (
						<ErrorState
							title="Couldn't load the event types"
							detail={loadFailureDetail(operations.error)}
						/>
					) : eventTypes.length === 0 ? (
						<EmptyState
							className="py-6"
							icon={<Workflow />}
							heading="No event types found."
							description="Event activity appears after an application starts publishing events."
						/>
					) : (
						<EventTypesTable rows={eventTypes} />
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle role="heading" aria-level={2} className="text-base">
						Subscriptions
					</CardTitle>
					<CardDescription>
						Every active subscription on the platform, with the number of events
						its queue could not deliver.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{subscriptions.isLoading ? (
						<Skeleton className="h-48 w-full" />
					) : subscriptions.isError ? (
						<ErrorState
							title="Couldn't load the subscriptions"
							detail={loadFailureDetail(subscriptions.error)}
						/>
					) : subscriptions.data?.subscriptions.length === 0 ? (
						<EmptyState
							className="py-6"
							icon={<Radio />}
							heading="No active subscriptions."
							description="Subscriptions appear when an application starts receiving events."
						/>
					) : (
						<SubscriptionsTable
							rows={subscriptions.data?.subscriptions ?? []}
						/>
					)}
				</CardContent>
			</Card>

			{!operations.isError && deadLetters.length > 0 && (
				<Card>
					<CardHeader>
						<CardTitle role="heading" aria-level={2} className="text-base">
							Undelivered events by queue
						</CardTitle>
						<CardDescription>
							Events set aside after every delivery attempt failed.
						</CardDescription>
					</CardHeader>
					<CardContent>
						<UndeliveredTable rows={deadLetters} />
					</CardContent>
				</Card>
			)}
		</PageBody>
	);
}

function EventTypesTable({ rows }: { rows: EventTypeSnapshot[] }) {
	const columns = useMemo(() => {
		const col = createColumnHelper<EventTypeSnapshot>();
		return [
			col.accessor("type", { header: "Event" }),
			col.accessor("visibility", {
				header: "Visibility",
				cell: ({ getValue }) => (
					<Badge variant={visibilityBadgeVariant(getValue())}>
						{eventVisibilityLabel(getValue())}
					</Badge>
				),
			}),
			col.accessor("publisher", {
				header: "Publisher",
				cell: ({ getValue }) => getValue() || "Unavailable",
			}),
			col.accessor("major", {
				header: "Version",
				cell: ({ getValue }) => (getValue() ? `v${getValue()}` : "Unavailable"),
			}),
			col.accessor("subscribers", {
				header: "Subscribers",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("totalEvents", {
				header: "Total",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("unpublished", {
				header: "Waiting",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("lastEventAt", {
				header: "Last event",
				cell: ({ getValue }) => optionalDate(getValue()),
			}),
		];
	}, []);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function SubscriptionsTable({ rows }: { rows: EventSubscriptionSummary[] }) {
	const columns = useMemo(() => {
		const col = createColumnHelper<EventSubscriptionSummary>();
		return [
			col.accessor("subscriberPrincipalId", {
				header: "Subscriber",
				cell: ({ getValue }) => truncateUUID(getValue()),
			}),
			col.accessor("typePattern", { header: "Events" }),
			col.accessor("queue", { header: "Queue" }),
			col.accessor("delivery", {
				header: "Delivery",
				cell: ({ getValue }) => <Badge variant="outline">{getValue()}</Badge>,
			}),
			col.accessor("queueDeadLetter", {
				header: "Undelivered",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
			col.accessor("createdAt", {
				header: "Created",
				cell: ({ getValue }) => optionalDate(getValue()),
			}),
		];
	}, []);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function UndeliveredTable({ rows }: { rows: QueueDeadLetters[] }) {
	const columns = useMemo(() => {
		const col = createColumnHelper<QueueDeadLetters>();
		return [
			col.accessor("queue", { header: "Queue" }),
			col.accessor("deadLetter", {
				header: "Undelivered",
				cell: ({ getValue }) => getValue().toLocaleString(),
			}),
		];
	}, []);
	const table = useReactTable({
		data: rows,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
	});
	return <DataTable table={table} />;
}

function MetricCard({
	title,
	value,
	description,
	warning = false,
}: {
	title: string;
	value: string;
	description: string;
	warning?: boolean;
}) {
	return (
		<Card>
			<CardHeader className="pb-2">
				<CardDescription>{title}</CardDescription>
				<CardTitle className={warning ? "text-destructive" : undefined}>
					{value}
				</CardTitle>
			</CardHeader>
			<CardContent className="text-xs text-muted-foreground">
				{description}
			</CardContent>
		</Card>
	);
}
