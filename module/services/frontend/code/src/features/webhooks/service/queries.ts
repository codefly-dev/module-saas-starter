import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { WebhookService } from "@/gen/user_pb";

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
