import { queryOptions } from "@tanstack/react-query";

// TODO: Replace with real Connect RPC client when GDPRService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { GDPRService } from "@/gen/user_pb";
// const client = createClient(GDPRService, apiTransport);

export const gdprQueries = {
  requests: () =>
    queryOptions({
      queryKey: ["gdpr", "requests"],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.listGDPRRequests({});
        return { requests: [] as never[] };
      },
    }),
};
