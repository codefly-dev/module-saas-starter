import type { EventRelayHealth } from "@/gen/saas/events/v1/operations_pb";

/** What a status says; the kit's Badge `tone`. */
export type StatusTone = "neutral" | "success" | "warning" | "danger" | "info";

// Catalog visibility is a free-form string carried from the declared event
// catalog (internal | tenant | external). A type that was published without
// being declared arrives with an empty visibility.
const VISIBILITY: Record<string, { label: string; tone: StatusTone }> = {
	internal: { label: "Internal", tone: "neutral" },
	tenant: { label: "Tenant", tone: "neutral" },
	external: { label: "External", tone: "info" },
	"": { label: "Undeclared", tone: "warning" },
};

/** A visibility this build has no word for reads "Other", never the raw value. */
export function eventVisibilityLabel(visibility: string): string {
	return VISIBILITY[visibility]?.label ?? "Other";
}

export function eventVisibilityTone(visibility: string): StatusTone {
	return VISIBILITY[visibility]?.tone ?? "neutral";
}

const DELIVERY: Record<string, string> = {
	ordered: "In order",
	unordered: "Any order",
};

/** Whether a subscription receives its events in the order they happened. */
export function deliveryOrderLabel(delivery: string): string {
	return DELIVERY[delivery] ?? "Not recognized";
}

export interface RelayTile {
	id: "recorded" | "sent" | "waiting";
	label: string;
	/** `null` when there is no answer: it draws a dash, never a zero. */
	value: number | null;
}

type RelayCounts = Pick<
	EventRelayHealth,
	"totalEvents" | "publishedEvents" | "backlog"
>;

/** The three headline counts of event delivery. */
export function relayTiles(relay: RelayCounts | undefined): RelayTile[] {
	return [
		{
			id: "recorded",
			label: "Events recorded",
			value: relay ? Number(relay.totalEvents) : null,
		},
		{
			id: "sent",
			label: "Sent to subscribers",
			value: relay ? Number(relay.publishedEvents) : null,
		},
		{
			id: "waiting",
			label: "Waiting to be sent",
			value: relay ? Number(relay.backlog) : null,
		},
	];
}

/**
 * What to say when delivery is behind, or `null` when nothing is waiting.
 * `oldest` is when the oldest waiting event was recorded, already formatted.
 */
export function backlogNotice(
	waiting: number,
	oldest: string | null,
): string | null {
	if (waiting <= 0) return null;
	const count =
		waiting === 1
			? "1 event is waiting to be sent"
			: `${waiting.toLocaleString("en-US")} events are waiting to be sent`;
	return oldest
		? `Delivery is behind: ${count}, the oldest since ${oldest}.`
		: `Delivery is behind: ${count}.`;
}
