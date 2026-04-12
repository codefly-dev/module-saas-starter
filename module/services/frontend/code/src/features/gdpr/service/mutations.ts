// TODO: Replace with real Connect RPC client when GDPRService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { GDPRService } from "@/gen/user_pb";
// const client = createClient(GDPRService, apiTransport);

export const gdprMutations = {
  requestExport: async () => {
    // TODO: Replace with real RPC call
    // return client.requestDataExport({});
    return { requestId: crypto.randomUUID(), status: "pending" };
  },

  requestDeletion: async () => {
    // TODO: Replace with real RPC call
    // return client.requestAccountDeletion({});
    return { requestId: crypto.randomUUID(), status: "pending" };
  },

  getExportStatus: async (requestId: string) => {
    // TODO: Replace with real RPC call
    // return client.getExportStatus({ requestId });
    return { requestId, status: "pending", downloadUrl: undefined };
  },
};
