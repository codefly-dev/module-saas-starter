import { queryOptions } from "@tanstack/react-query";

// TODO: Replace with real Connect RPC client when WebhookService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { WebhookService } from "@/gen/user_pb";
// const client = createClient(WebhookService, apiTransport);

export const webhookQueries = {
  subscriptions: (orgId: string) =>
    queryOptions({
      queryKey: ["webhooks", orgId],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.listWebhookSubscriptions({ orgId });
        return { subscriptions: [] as never[] };
      },
      enabled: !!orgId,
    }),

  deliveries: (subscriptionId: string) =>
    queryOptions({
      queryKey: ["webhook-deliveries", subscriptionId],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.listWebhookDeliveries({ subscriptionId });
        return { deliveries: [] as never[] };
      },
      enabled: !!subscriptionId,
    }),
};
