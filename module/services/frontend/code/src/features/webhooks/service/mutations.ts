import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { WebhookService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(WebhookService, apiTransport);

export const webhookMutations = {
  create: (orgId: string, url: string, events: string[], description?: string) =>
    client.createSubscription({ orgId, url, events, description }),

  delete: (id: string) =>
    client.deleteSubscription({ id }),

  // test now accepts an optional event_type so the FE picker can fire
  // a sample event of any subscribed type — same envelope shape as
  // the production async dispatcher emits.
  test: (id: string, eventType?: string) =>
    client.testWebhook({ id, eventType: eventType ?? "" }),

  // replay re-fires an existing delivery's payload at the
  // subscription's CURRENT URL. Returns the new delivery row so the
  // FE can show the outcome without polling.
  replay: (deliveryId: string) =>
    client.replayDelivery({ id: deliveryId }),

  // rotateSecret returns the new HMAC secret ONCE — caller must show
  // it inline with a copy button and a "this is your last chance"
  // warning. The old secret stops working immediately.
  rotateSecret: (id: string) =>
    client.rotateSecret({ id }),

  // getDelivery refreshes a single row after a replay or status
  // change. Lighter than re-listing.
  getDelivery: (deliveryId: string) =>
    client.getDelivery({ id: deliveryId }),
};
