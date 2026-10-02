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

// The names an endpoint may subscribe to come from the server-owned audit
// registry (AuditService/ListAuditEventTypes, `webhookEligible`), never from a
// list kept here.
//
// The list that used to live here offered four names the catalog never had —
// saas.org.updated, saas.org.deleted, saas.invite.sent and saas.invite.accepted;
// the catalog has saas.invitation.* — so an endpoint subscribed to them through
// this form silently never received anything. It also could not offer the types
// a solution or a composed module declares, which are registered at runtime and
// cannot be known to a list compiled into this build. Both failures are the same
// one: a client deciding what the registry alone can answer.
