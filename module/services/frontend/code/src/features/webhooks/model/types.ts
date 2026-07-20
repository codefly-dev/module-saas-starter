/** Clean domain types for the Webhooks feature — decoupled from protobuf. */

export type WebhookDeliveryStatus =
	| "pending"
	| "success"
	| "failed";

export interface WebhookSubscription {
	id: string;
	orgId: string;
	url: string;
	description: string;
	events: string[];
	active: boolean;
	createdAt: string | undefined;
	updatedAt: string | undefined;
}

export interface WebhookDelivery {
	id: string;
	subscriptionId: string;
	event: string;
	status: WebhookDeliveryStatus;
	httpStatus: number | undefined;
	attempts: number;
	lastAttemptAt: string | undefined;
	createdAt: string | undefined;
	// payload (request body) and responseBody (consumer's reply, capped
	// at 4KiB server-side). Both are populated for any delivery that
	// actually attempted an HTTP round-trip; empty for "pending" rows.
	payload: string;
	responseBody: string;
}

export const WEBHOOK_EVENT_TYPES = [
	"user.created",
	"user.updated",
	"user.deleted",
	"org.created",
	"org.updated",
	"org.deleted",
	"team.member.added",
	"team.member.removed",
	"invite.sent",
	"invite.accepted",
	"api_key.created",
	"api_key.revoked",
] as const;

export type WebhookEventType = (typeof WEBHOOK_EVENT_TYPES)[number];
