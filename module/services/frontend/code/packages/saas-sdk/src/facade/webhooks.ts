import { type Client, createClient, type Transport } from "@connectrpc/connect";
import { WebhookService } from "../gen/saas/accounts/v1/webhooks_pb.js";

export type Webhooks = Client<typeof WebhookService>;

export function New(gw: Transport): Webhooks {
	return createClient(WebhookService, gw);
}
