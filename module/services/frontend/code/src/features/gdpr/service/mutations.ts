import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { GDPRService } from "@/gen/user_pb";

const client = createClient(GDPRService, apiTransport);

export const gdprMutations = {
  requestExport: () =>
    client.requestExport({}),

  requestDeletion: () =>
    client.requestDeletion({}),
};
