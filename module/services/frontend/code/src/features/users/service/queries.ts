import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { PlatformAdminService, UserService } from "@/gen/user_pb";

const adminClient = createClient(PlatformAdminService, apiTransport);
const userClient = createClient(UserService, apiTransport);

export const userQueries = {
  list: (query?: string) =>
    queryOptions({
      queryKey: ["users", query ?? ""],
      queryFn: () =>
        adminClient.searchUsers({ query: query ?? "", pageSize: 50 }),
    }),

  detail: (uuid: string) =>
    queryOptions({
      queryKey: ["users", uuid],
      queryFn: () =>
        userClient.getUser({ identifier: { case: "uuid", value: uuid } }),
      enabled: !!uuid,
    }),
};
