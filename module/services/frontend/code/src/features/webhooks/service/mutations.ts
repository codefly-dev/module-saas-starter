import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { WebhookService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(WebhookService, apiTransport);

export const webhookMutations = {
  create: (orgId: string, url: string, events: string[], description?: string) =>
    client.createSubscription({ orgId, url, events, description }),

  delete: (id: string) =>
    client.deleteSubscription({ id }),

  test: (id: string) =>
    client.testWebhook({ id }),
};
