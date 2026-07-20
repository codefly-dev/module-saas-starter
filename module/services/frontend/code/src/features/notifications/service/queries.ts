import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { NotificationService } from "@/gen/saas/accounts/v1/notifications_pb";
import { apiTransport } from "@/lib/connect/transport";

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
