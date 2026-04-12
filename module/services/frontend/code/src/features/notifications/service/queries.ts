import { queryOptions } from "@tanstack/react-query";

// TODO: Replace with real Connect RPC client when NotificationService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { NotificationService } from "@/gen/user_pb";
// const client = createClient(NotificationService, apiTransport);

export const notificationQueries = {
  list: (pageSize = 20) =>
    queryOptions({
      queryKey: ["notifications", pageSize],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.listNotifications({ pageSize });
        return { notifications: [] as never[], totalCount: 0 };
      },
    }),

  unreadCount: () =>
    queryOptions({
      queryKey: ["notifications", "unread-count"],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.getUnreadCount({});
        return { count: 0 };
      },
      refetchInterval: 30_000,
    }),
};
