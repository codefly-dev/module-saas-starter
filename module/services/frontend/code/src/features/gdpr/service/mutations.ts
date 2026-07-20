import { createClient } from "@connectrpc/connect";
import { GDPRService } from "@/gen/saas/accounts/v1/privacy_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(GDPRService, apiTransport);

export const gdprMutations = {
	requestExport: () => client.requestExport({}),

	requestDeletion: () => client.requestDeletion({}),
};
