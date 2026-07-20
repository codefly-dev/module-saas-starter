import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { WebhookSubscription as WebhookSubscriptionMessage } from "@/gen/saas/accounts/v1/webhooks_pb";
import type { WebhookDeliveryStatus, WebhookSubscription } from "./types";

type BadgeVariant = "default" | "secondary" | "destructive";

export function formatDeliveryStatus(status: WebhookDeliveryStatus): {
	label: string;
	variant: BadgeVariant;
} {
	switch (status) {
		case "success":
			return { label: "Success", variant: "default" };
		case "pending":
			return { label: "Pending", variant: "secondary" };
		case "failed":
			return { label: "Failed", variant: "destructive" };
		default:
			return { label: "Unknown", variant: "secondary" };
	}
}

export function formatEventType(event: string): string {
	return event
		.split(".")
		.map((part) => part.charAt(0).toUpperCase() + part.slice(1))
		.join(" ");
}

/**
 * Convert the generated protobuf transport message at the feature boundary.
 * Keeping protobuf Timestamp objects out of view models prevents React from
 * trying to render `{ seconds, nanos }` as a child.
 */
export function toWebhookSubscription(
	subscription: WebhookSubscriptionMessage,
): WebhookSubscription {
	return {
		id: subscription.id,
		orgId: subscription.orgId,
		url: subscription.url,
		description: subscription.description,
		events: [...subscription.events],
		active: subscription.active,
		createdAt: subscription.createdAt
			? timestampDate(subscription.createdAt).toISOString()
			: undefined,
		updatedAt: undefined,
	};
}
