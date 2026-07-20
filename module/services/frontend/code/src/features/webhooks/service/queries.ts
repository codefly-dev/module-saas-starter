import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { WebhookService } from "@/gen/saas/accounts/v1/webhooks_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(WebhookService, apiTransport);

export const webhookQueries = {
	subscriptions: (orgId: string) =>
		queryOptions({
			queryKey: ["webhooks", orgId],
			queryFn: () => client.listSubscriptions({ orgId }),
			enabled: !!orgId,
		}),

	deliveries: (subscriptionId: string) =>
		queryOptions({
			queryKey: ["webhook-deliveries", subscriptionId],
			queryFn: () => client.listDeliveries({ subscriptionId }),
			enabled: !!subscriptionId,
		}),
};
