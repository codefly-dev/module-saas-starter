import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { NotificationService } from "@/gen/user_pb";

const client = createClient(NotificationService, apiTransport);

export const notificationQueries = {
  list: (pageSize = 20) =>
    queryOptions({
      queryKey: ["notifications", pageSize],
      queryFn: () => client.listNotifications({ pageSize }),
    }),

  unreadCount: () =>
    queryOptions({
      queryKey: ["notifications", "unread-count"],
      queryFn: () => client.getUnreadCount({}),
      refetchInterval: 30_000,
    }),
};
