"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
	ChevronDown,
	ChevronUp,
	Clipboard,
	ClipboardCheck,
	RotateCcw,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardHeader,
	CardTitle,
	Skeleton,
} from "@/shared/ui";
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

	const { data, isLoading } = useQuery(
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
					"A new delivery was created. Check the latest row for its worker-projected outcome.",
			});
			void queryClient.invalidateQueries({
				queryKey: ["webhook-deliveries", subscription.id],
			});
		},
		onError: (err) =>
			toast.error("Failed to queue replay", { description: err.message }),
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
							{Array.from({ length: 3 }).map((_, i) => (
								<Skeleton key={i} className="h-12 w-full" />
							))}
						</div>
					) : deliveries.length === 0 ? (
						<p className="text-sm text-muted-foreground py-8 text-center">
							No deliveries yet. Use <span className="font-medium">Test</span>{" "}
							on the subscription to fire a sample event.
						</p>
					) : (
						<div className="grid gap-4 lg:grid-cols-[260px_1fr]">
							{/* LEFT — list */}
							<div className="space-y-1.5 lg:max-h-[480px] lg:overflow-y-auto pr-1">
								{deliveries.map((d) => (
									<DeliveryRow
										key={d.id}
										delivery={d}
										selected={selected?.id === d.id}
										onClick={() => setSelectedId(d.id)}
									/>
								))}
							</div>

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

function DeliveryRow({
	delivery,
	selected,
	onClick,
}: {
	delivery: RowDelivery;
	selected: boolean;
	onClick: () => void;
}) {
	const status = statusFromProto(delivery.status);
	const { label, variant } = formatDeliveryStatus(status);
	return (
		<button
			onClick={onClick}
			aria-label={`Delivery ${delivery.id}: ${formatEventType(delivery.eventType)}`}
			className={`w-full text-left rounded-md border p-2.5 text-xs transition-colors ${
				selected ? "border-primary/60 bg-accent/40" : "hover:bg-accent/20"
			}`}
		>
			<div className="flex items-center justify-between gap-2">
				<Badge variant={variant} className="shrink-0 text-[10px]">
					{label}
				</Badge>
				{delivery.httpStatus > 0 && (
					<span
						className={`font-mono ${
							delivery.httpStatus >= 200 && delivery.httpStatus < 300
								? "text-emerald-600 dark:text-emerald-400"
								: "text-red-600 dark:text-red-400"
						}`}
					>
						{delivery.httpStatus}
					</span>
				)}
			</div>
			<div className="mt-1 truncate font-medium">
				{formatEventType(delivery.eventType)}
			</div>
			<div className="mt-0.5 text-muted-foreground">
				{formatDate(timestampToISO(delivery.createdAt))}
			</div>
		</button>
	);
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
					<div className="text-xs text-muted-foreground">Delivery ID</div>
					<div className="font-mono text-xs">{delivery.id}</div>
					<div className="mt-1 text-xs text-muted-foreground">Event ID</div>
					<div className="font-mono text-xs">{delivery.eventId}</div>
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
				<dd className="font-mono">{delivery.eventType}</dd>
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
			<Section
				title="Request payload"
				body={delivery.payload || ""}
				prettyJson
			/>

			{/* Response body */}
			<Section
				title="Response body"
				body={delivery.responseBody || ""}
				emptyHint="Endpoint returned no body, or no attempt has been made yet."
			/>

		</div>
	);
}

function Section({
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
	const [copied, setCopied] = useState(false);
	const formatted = prettyJson ? tryPrettyJson(body) : body;
	const empty = !body;

	async function copy() {
		try {
			await navigator.clipboard.writeText(body);
			setCopied(true);
			toast.success("Copied");
			window.setTimeout(() => setCopied(false), 1500);
		} catch {
			toast.error("Copy failed");
		}
	}

	return (
		<div>
			<div className="flex items-center justify-between mb-1.5">
				<div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
					{title}
				</div>
				{!empty && (
					<Button variant="ghost" size="sm" className="h-7 px-2" onClick={copy}>
						{copied ? (
							<ClipboardCheck className="h-3.5 w-3.5" />
						) : (
							<Clipboard className="h-3.5 w-3.5" />
						)}
						<span className="ml-1.5 text-xs">Copy</span>
					</Button>
				)}
			</div>
			{empty ? (
				<div className="text-xs text-muted-foreground italic">
					{emptyHint ?? "Empty."}
				</div>
			) : (
				<pre className="max-h-64 overflow-auto rounded-md border bg-muted/30 p-3 text-[11px] font-mono leading-relaxed whitespace-pre-wrap break-all">
					{formatted}
				</pre>
			)}
		</div>
	);
}

// ─────────────────────────────────────────────────────────────

function statusFromProto(
	s: number,
): "pending" | "success" | "failed" {
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

function tryPrettyJson(s: string): string {
	if (!s) return "";
	try {
		return JSON.stringify(JSON.parse(s), null, 2);
	} catch {
		return s;
	}
}

function timestampToISO(t?: { seconds: bigint }): string | undefined {
	if (!t) return undefined;
	return new Date(Number(t.seconds) * 1000).toISOString();
}
