import { queryOptions } from "@tanstack/react-query";

// TODO: Replace with real Connect RPC client when MFAService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { MFAService } from "@/gen/user_pb";
// const client = createClient(MFAService, apiTransport);

export const mfaQueries = {
  devices: () =>
    queryOptions({
      queryKey: ["mfa", "devices"],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.listMFADevices({});
        return { devices: [] as never[] };
      },
    }),
};
