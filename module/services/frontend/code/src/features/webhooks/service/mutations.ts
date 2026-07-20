import { createClient } from "@connectrpc/connect";
import { WebhookService } from "@/gen/saas/accounts/v1/webhooks_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(WebhookService, apiTransport);

export const webhookMutations = {
	create: (
		orgId: string,
		url: string,
		events: string[],
		description?: string,
	) => client.createSubscription({ orgId, url, events, description }),

	delete: (id: string) => client.deleteSubscription({ id }),

	// Test and replay create delivery history plus a generated outbox job. The
	// generic worker performs the network request after the RPC returns.
	test: (id: string, eventType?: string) =>
		client.testWebhook({ id, eventType: eventType ?? "" }),

	// Replay preserves the exact event payload and returns its new pending row.
	replay: (deliveryId: string) => client.replayDelivery({ id: deliveryId }),

	// Keep the previous key in the signature header for 24 hours so a consumer
	// can deploy verification with the new key before removing the old one.
	rotateSecret: (id: string) =>
		client.rotateSecret({ id, gracePeriodSeconds: 24 * 60 * 60 }),

	// getDelivery refreshes a single row after a replay or status
	// change. Lighter than re-listing.
	getDelivery: (deliveryId: string) => client.getDelivery({ id: deliveryId }),
};
