// TODO: Replace with real Connect RPC client when WebhookService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { WebhookService } from "@/gen/user_pb";
// const client = createClient(WebhookService, apiTransport);

export const webhookMutations = {
  create: async (orgId: string, url: string, events: string[], description?: string) => {
    // TODO: Replace with real RPC call
    // return client.createWebhookSubscription({ orgId, url, events, description });
    return { id: crypto.randomUUID(), orgId, url, events, description };
  },

  delete: async (subscriptionId: string) => {
    // TODO: Replace with real RPC call
    // return client.deleteWebhookSubscription({ subscriptionId });
    return {};
  },

  test: async (subscriptionId: string) => {
    // TODO: Replace with real RPC call
    // return client.testWebhookSubscription({ subscriptionId });
    return { delivered: true };
  },
};
