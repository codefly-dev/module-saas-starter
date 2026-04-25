import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { UserSettingsService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(UserSettingsService, apiTransport);

export const userSettingsQueries = {
  current: () =>
    queryOptions({
      queryKey: ["user-settings"],
      queryFn: () => client.get({}),
      // Settings change rarely; the 30s default is fine, but bump to
      // 5 min so navigation between settings sub-pages doesn't refetch.
      staleTime: 5 * 60 * 1000,
    }),
};
