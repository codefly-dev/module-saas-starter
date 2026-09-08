"use client";

import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { AlertTriangle, Radio, RefreshCw, Workflow } from "lucide-react";
import { useMemo } from "react";
import type {
	EventSubscriptionSummary,
	EventTypeSnapshot,
} from "@/gen/saas/events/v1/operations_pb";
import { formatDate, truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Skeleton,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
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

export function EventOperationsPage() {
	const operations = useEventOperations();
	const subscriptions = useEventSubscriptions();

	const relay = operations.data?.relay;
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
		<div className="space-y-6">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Event operations</h1>
					<p className="text-muted-foreground">
						Domain-event catalog, subscribers, relay lag, and dead letters across
						the platform. Payloads never cross this surface.
					</p>
				</div>
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
			</div>

			<div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
				<MetricCard
					title="Total events"
					value={(relay?.totalEvents ?? BigInt(0)).toLocaleString()}
					description="Durable outbox rows"
				/>
				<MetricCard
					title="Published"
					value={(relay?.publishedEvents ?? BigInt(0)).toLocaleString()}
					description="Fanned out by the relay"
				/>
				<MetricCard
					title="Backlog"
					value={backlog.toLocaleString()}
					description="Awaiting fan-out"
					warning={backlog !== BigInt(0)}
				/>
				<MetricCard
					title="Head-of-line lag"
					value={optionalDate(relay?.oldestUnpublishedAt)}
					description="Oldest unpublished event"
					warning={Boolean(relay?.oldestUnpublishedAt)}
				/>
			</div>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Event types</CardTitle>
					<CardDescription>
						Declared catalog types merged with outbox counters. Undeclared
						emitters are surfaced so nothing can hide.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{operations.isLoading ? (
						<Skeleton className="h-48 w-full" />
					) : operations.isError ? (
						<div className="flex items-center gap-2 text-sm text-destructive">
							<AlertTriangle className="h-4 w-4" />
							Unable to load event operations.
						</div>
					) : eventTypes.length === 0 ? (
						<div className="flex flex-col items-center gap-2 py-10 text-muted-foreground">
							<Workflow className="h-8 w-8" />
							<p className="text-sm">No event types found.</p>
						</div>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Type</TableHead>
									<TableHead>Visibility</TableHead>
									<TableHead>Publisher</TableHead>
									<TableHead>Major</TableHead>
									<TableHead>Subscribers</TableHead>
									<TableHead>Total</TableHead>
									<TableHead>Unpublished</TableHead>
									<TableHead>Last event</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{eventTypes.map((snapshot) => (
									<EventTypeRow key={snapshot.type} snapshot={snapshot} />
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle className="text-base">Subscriptions</CardTitle>
					<CardDescription>
						Live (non-revoked) control-plane subscriptions across all principals,
						each with its queue dead-letter depth.
					</CardDescription>
				</CardHeader>
				<CardContent>
					{subscriptions.isLoading ? (
						<Skeleton className="h-48 w-full" />
					) : subscriptions.isError ? (
						<div className="flex items-center gap-2 text-sm text-destructive">
							<AlertTriangle className="h-4 w-4" />
							Unable to load subscriptions.
						</div>
					) : subscriptions.data?.subscriptions.length === 0 ? (
						<div className="flex flex-col items-center gap-2 py-10 text-muted-foreground">
							<Radio className="h-8 w-8" />
							<p className="text-sm">No live subscriptions.</p>
						</div>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Subscriber</TableHead>
									<TableHead>Pattern</TableHead>
									<TableHead>Queue</TableHead>
									<TableHead>Delivery</TableHead>
									<TableHead>Dead letter</TableHead>
									<TableHead>Created</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{subscriptions.data?.subscriptions.map((sub) => (
									<SubscriptionRow key={sub.id} subscription={sub} />
								))}
							</TableBody>
						</Table>
					)}
				</CardContent>
			</Card>

			{deadLetters.length > 0 && (
				<Card>
					<CardHeader>
						<CardTitle className="text-base">Dead letters by queue</CardTitle>
						<CardDescription>
							Exhausted deliveries parked on live subscriber queues.
						</CardDescription>
					</CardHeader>
					<CardContent>
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Queue</TableHead>
									<TableHead>Dead letter</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{deadLetters.map((entry) => (
									<TableRow key={entry.queue}>
										<TableCell className="font-mono text-xs">
											{entry.queue}
										</TableCell>
										<TableCell
											className={
												entry.deadLetter !== BigInt(0)
													? "text-destructive"
													: undefined
											}
										>
											{entry.deadLetter.toLocaleString()}
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					</CardContent>
				</Card>
			)}
		</div>
	);
}

function EventTypeRow({ snapshot }: { snapshot: EventTypeSnapshot }) {
	return (
		<TableRow>
			<TableCell className="font-mono text-xs">{snapshot.type}</TableCell>
			<TableCell>
				<Badge variant={visibilityBadgeVariant(snapshot.visibility)}>
					{eventVisibilityLabel(snapshot.visibility)}
				</Badge>
			</TableCell>
			<TableCell className="font-mono text-xs">
				{snapshot.publisher || "-"}
			</TableCell>
			<TableCell>{snapshot.major ? `v${snapshot.major}` : "-"}</TableCell>
			<TableCell>{snapshot.subscribers.toLocaleString()}</TableCell>
			<TableCell>{snapshot.totalEvents.toLocaleString()}</TableCell>
			<TableCell
				className={
					snapshot.unpublished !== BigInt(0) ? "text-destructive" : undefined
				}
			>
				{snapshot.unpublished.toLocaleString()}
			</TableCell>
			<TableCell>{optionalDate(snapshot.lastEventAt)}</TableCell>
		</TableRow>
	);
}

function SubscriptionRow({
	subscription,
}: {
	subscription: EventSubscriptionSummary;
}) {
	return (
		<TableRow>
			<TableCell className="font-mono text-xs">
				{truncateUUID(subscription.subscriberPrincipalId)}
			</TableCell>
			<TableCell className="font-mono text-xs">
				{subscription.typePattern}
			</TableCell>
			<TableCell className="font-mono text-xs">{subscription.queue}</TableCell>
			<TableCell>
				<Badge variant="outline">{subscription.delivery}</Badge>
			</TableCell>
			<TableCell
				className={
					subscription.queueDeadLetter !== BigInt(0)
						? "text-destructive"
						: undefined
				}
			>
				{subscription.queueDeadLetter.toLocaleString()}
			</TableCell>
			<TableCell>{optionalDate(subscription.createdAt)}</TableCell>
		</TableRow>
	);
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
