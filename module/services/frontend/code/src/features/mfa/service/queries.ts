import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { MFAService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(MFAService, apiTransport);

export const mfaQueries = {
  devices: () =>
    queryOptions({
      queryKey: ["mfa", "devices"],
      queryFn: () => client.listDevices({}),
    }),
};
