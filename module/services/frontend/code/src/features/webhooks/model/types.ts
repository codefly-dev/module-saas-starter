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

// Webhook fan-out routes on the audit event type (DurableAuditEmitter.write), so
// a subscription only ever fires for a name the audit registry mints — which is
// namespaced from issue #520 on.
export const WEBHOOK_EVENT_TYPES = [
	"saas.user.created",
	"saas.user.updated",
	"saas.user.deleted",
	"saas.org.created",
	"saas.org.updated",
	"saas.org.deleted",
	"saas.team.member.added",
	"saas.team.member.removed",
	"saas.invite.sent",
	"saas.invite.accepted",
	"saas.api_key.created",
	"saas.api_key.revoked",
] as const;

export type WebhookEventType = (typeof WEBHOOK_EVENT_TYPES)[number];
