"use client";

import { Content } from "@codefly-dev/ui/content";
import { EmptyState } from "@codefly-dev/ui/layout";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	type ColumnDef,
	getCoreRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ChevronDown, ChevronUp, RotateCcw } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	ErrorState,
	Skeleton,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { formatDeliveryStatus, formatEventType } from "../model/transforms";
import type { WebhookSubscription } from "../model/types";
import { webhookMutations } from "../service/mutations";
import { webhookQueries } from "../service/queries";

interface WebhookDeliveriesPanelProps {
	subscription: WebhookSubscription;
	onClose: () => void;
}

/**
 * WebhookDeliveriesPanel — Stripe-style two-column inspector:
 *   left rail = list of recent attempts, ordered newest first.
 *   right rail = detail for the selected row (defaults to newest):
 *     event type, status, attempt count, request payload (pretty
 *     JSON), captured response body, replay button, copy buttons.
 *
 * Status maps the customer-visible outcome: green for 2xx, neutral before the
 * first attempt, and red for the latest failed attempt. Generic job operations
 * own retry/dead-letter state. Replay creates a new delivery row at
 * the subscription's CURRENT URL — recovers from the common "wrong
 * endpoint at delivery time, fixed since" failure mode.
 */
export function WebhookDeliveriesPanel({
	subscription,
	onClose,
}: WebhookDeliveriesPanelProps) {
	const [expanded, setExpanded] = useState(true);
	const [selectedId, setSelectedId] = useState<string | null>(null);
	const queryClient = useQueryClient();

	const { data, isLoading, isError, error, refetch } = useQuery(
		webhookQueries.deliveries(subscription.id),
	);

	// Connect-ES returns the proto message verbatim — `eventType` /
	// `httpStatus` / `responseBody` come through camelCased; we don't
	// run a domain transform here so this view stays in sync if the
	// proto evolves.
	type RawDelivery = {
		id: string;
		eventType: string;
		status: number; // proto enum: pending/success/failed
		httpStatus: number;
		attempts: number;
		payload: string;
		responseBody: string;
		eventId: string;
		createdAt?: { seconds: bigint };
		deliveredAt?: { seconds: bigint };
		lastAttemptAt?: { seconds: bigint };
	};

	const deliveries: RawDelivery[] =
		(data as { deliveries?: RawDelivery[] } | undefined)?.deliveries ?? [];
	const selected = selectedId
		? deliveries.find((d) => d.id === selectedId)
		: deliveries[0];

	const replay = useMutation({
		mutationFn: (deliveryId: string) => webhookMutations.replay(deliveryId),
		onSuccess: () => {
			toast.success("Replay queued", {
				description:
					"A new delivery was created. Its outcome appears on the newest row.",
			});
			void queryClient.invalidateQueries({
				queryKey: ["webhook-deliveries", subscription.id],
			});
		},
		onError: (error) => toast.error(failureText("replay the delivery", error)),
	});

	return (
		<Card role="region" aria-label={`Deliveries for ${subscription.url}`}>
			<CardHeader className="flex flex-row items-center justify-between py-3">
				<CardTitle className="text-base">
					Deliveries for{" "}
					<span className="font-mono text-sm">{subscription.url}</span>
				</CardTitle>
				<div className="flex items-center gap-2">
					<Button
						variant="ghost"
						size="sm"
						aria-label={expanded ? "Collapse deliveries" : "Expand deliveries"}
						aria-expanded={expanded}
						onClick={() => setExpanded(!expanded)}
					>
						{expanded ? (
							<ChevronUp className="h-4 w-4" />
						) : (
							<ChevronDown className="h-4 w-4" />
						)}
					</Button>
					<Button variant="ghost" size="sm" onClick={onClose}>
						Close
					</Button>
				</div>
			</CardHeader>

			{expanded && (
				<CardContent>
					{isLoading ? (
						<div className="space-y-2">
							{["first", "second", "third"].map((key) => (
								<Skeleton key={key} className="h-12 w-full" />
							))}
						</div>
					) : isError ? (
						// A failed read is not "no deliveries": whether any were
						// attempted is unknown.
						<div className="space-y-3">
							<ErrorState
								title="Couldn't load the deliveries"
								detail={
									readOutcome(true, error) === "forbidden"
										? "You don't have permission to see this webhook's deliveries. Ask an organization administrator for access."
										: "We couldn’t check recent deliveries. Try again in a moment."
								}
							/>
							{readOutcome(true, error) !== "forbidden" && (
								<Button variant="outline" onClick={() => void refetch()}>
									Try again
								</Button>
							)}
						</div>
					) : deliveries.length === 0 ? (
						<EmptyState
							heading="No deliveries yet"
							description="Send a test from the webhook’s actions menu to check that your endpoint receives events."
						/>
					) : (
						<div className="grid gap-4 grid-cols-1 xl:grid-cols-2">
							{/* LEFT — list */}
							<DeliveryTable
								deliveries={deliveries}
								selectedId={selected?.id}
								onSelect={setSelectedId}
							/>

							{/* RIGHT — detail */}
							{selected ? (
								<DeliveryDetail
									delivery={selected}
									onReplay={() => replay.mutate(selected.id)}
									isReplaying={replay.isPending}
								/>
							) : (
								<div className="text-sm text-muted-foreground py-8 text-center">
									Select a delivery to inspect.
								</div>
							)}
						</div>
					)}
				</CardContent>
			)}
		</Card>
	);
}

interface RowDelivery {
	id: string;
	eventType: string;
	status: number;
	httpStatus: number;
	attempts: number;
	createdAt?: { seconds: bigint };
}

function DeliveryTable({
	deliveries,
	selectedId,
	onSelect,
}: {
	deliveries: RowDelivery[];
	selectedId?: string;
	onSelect: (id: string) => void;
}) {
	const columns = useMemo<ColumnDef<RowDelivery>[]>(
		() => [
			{
				id: "event",
				header: "Event",
				cell: ({ row }) => (
					<Button
						variant="ghost"
						size="sm"
						aria-pressed={selectedId === row.original.id}
						aria-label={`Delivery: ${formatEventType(row.original.eventType)}`}
						onClick={() => onSelect(row.original.id)}
					>
						{formatEventType(row.original.eventType)}
					</Button>
				),
			},
			{
				id: "status",
				header: "Status",
				cell: ({ row }) => {
					const { label, variant } = formatDeliveryStatus(
						statusFromProto(row.original.status),
					);
					return <Badge variant={variant}>{label}</Badge>;
				},
			},
			{
				id: "created",
				header: "Created",
				cell: ({ row }) => formatDate(timestampToISO(row.original.createdAt)),
			},
		],
		[selectedId, onSelect],
	);
	const table = useReactTable({
		data: deliveries,
		columns,
		getCoreRowModel: getCoreRowModel(),
	});
	return <DataTable table={table} ariaLabel="Webhook deliveries" />;
}

interface DetailDelivery extends RowDelivery {
	payload: string;
	responseBody: string;
	eventId: string;
	deliveredAt?: { seconds: bigint };
	lastAttemptAt?: { seconds: bigint };
}

function DeliveryDetail({
	delivery,
	onReplay,
	isReplaying,
}: {
	delivery: DetailDelivery;
	onReplay: () => void;
	isReplaying: boolean;
}) {
	return (
		<div className="space-y-4">
			{/* Header — id + replay */}
			<div className="flex items-center justify-between">
				<div>
					<div className="font-medium">
						{formatEventType(delivery.eventType)}
					</div>
					<details className="text-xs text-muted-foreground">
						<summary className="cursor-pointer">Technical details</summary>
						<div>
							Delivery: <code>{delivery.id}</code>
						</div>
						<div>
							Event: <code>{delivery.eventId}</code>
						</div>
					</details>
				</div>
				<Button
					size="sm"
					variant="outline"
					onClick={onReplay}
					disabled={isReplaying}
				>
					<RotateCcw className="mr-2 h-3.5 w-3.5" />
					{isReplaying ? "Replaying…" : "Replay"}
				</Button>
			</div>

			{/* Quick facts */}
			<dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
				<dt className="text-muted-foreground">Event</dt>
				<dd>{formatEventType(delivery.eventType)}</dd>
				<dt className="text-muted-foreground">HTTP status</dt>
				<dd className="font-mono">
					{delivery.httpStatus || (
						<span className="text-muted-foreground">—</span>
					)}
				</dd>
				<dt className="text-muted-foreground">Attempts</dt>
				<dd>{delivery.attempts}</dd>
				<dt className="text-muted-foreground">Created</dt>
				<dd>{formatDate(timestampToISO(delivery.createdAt))}</dd>
				{delivery.deliveredAt && (
					<>
						<dt className="text-muted-foreground">Delivered</dt>
						<dd>{formatDate(timestampToISO(delivery.deliveredAt))}</dd>
					</>
				)}
				{delivery.lastAttemptAt && (
					<>
						<dt className="text-muted-foreground">Last attempt</dt>
						<dd>{formatDate(timestampToISO(delivery.lastAttemptAt))}</dd>
					</>
				)}
			</dl>

			{/* Request payload */}
			<PayloadBlock
				title="Request payload"
				body={delivery.payload || ""}
				prettyJson
			/>

			{/* Response body */}
			<PayloadBlock
				title="Response body"
				body={delivery.responseBody || ""}
				emptyHint="Endpoint returned no body, or no attempt has been made yet."
			/>
		</div>
	);
}

// A labelled block of request/response text inside the panel. Named for what
// it is rather than `Section`, which in the kit is a page section with its own
// heading and actions row.
function PayloadBlock({
	title,
	body,
	emptyHint,
	prettyJson,
}: {
	title: string;
	body: string;
	emptyHint?: string;
	prettyJson?: boolean;
}) {
	const empty = !body;

	return (
		<div>
			<div className="mb-1.5 text-xs font-medium text-muted-foreground">
				{title}
			</div>
			{empty ? (
				<div className="text-xs text-muted-foreground italic">
					{emptyHint ?? "Empty."}
				</div>
			) : prettyJson ? (
				// A payload that is not valid JSON renders verbatim as text.
				<Content
					value={body}
					format="json"
					label={title}
					wrap
					className="max-h-64 overflow-auto"
				/>
			) : (
				// An endpoint's response is whatever it chose to send: shown as
				// text, with its whitespace, and never interpreted as markup.
				<Content
					value={body}
					format="code"
					wrap
					className="max-h-64 overflow-auto"
				/>
			)}
		</div>
	);
}

// ─────────────────────────────────────────────────────────────

function statusFromProto(s: number): "pending" | "success" | "failed" {
	// WebhookDeliveryStatus enum (webhooks.proto):
	//   0 UNSPECIFIED, 1 PENDING, 2 SUCCESS, 3 FAILED.
	switch (s) {
		case 2:
			return "success";
		case 3:
			return "failed";
		default:
			return "pending";
	}
}

function timestampToISO(t?: { seconds: bigint }): string | undefined {
	if (!t) return undefined;
	return new Date(Number(t.seconds) * 1000).toISOString();
}
