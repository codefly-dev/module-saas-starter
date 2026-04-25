import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { GDPRService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(GDPRService, apiTransport);

export const gdprMutations = {
  requestExport: () =>
    client.requestExport({}),

  requestDeletion: () =>
    client.requestDeletion({}),
};
