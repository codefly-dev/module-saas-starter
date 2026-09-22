import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { NotificationService } from "@/gen/saas/accounts/v1/notifications_pb";
import { apiTransport } from "@/lib/connect/transport";
import { toNotification } from "../model/transforms";

const client = createClient(NotificationService, apiTransport);

export const notificationQueries = {
	list: (
		pageSize = 20,
		options: {
			orgId?: string;
			unreadOnly?: boolean;
			pageToken?: string;
			userId?: string;
			firstVisible?: boolean;
		} = {},
	) =>
		queryOptions({
			queryKey: ["notifications", pageSize, options],
			queryFn: async () => {
				let pageToken = options.pageToken ?? "";
				let response;
				do {
					response = await client.listNotifications({
						pageSize,
						pageToken,
						orgId: options.orgId,
						unreadOnly: options.unreadOnly,
					});
					pageToken = response.nextPageToken;
				} while (
					options.firstVisible &&
					response.notifications.length === 0 &&
					pageToken
				);

				return {
					notifications: response.notifications.map(toNotification),
					nextPageToken: response.nextPageToken,
				};
			},
		}),

	unreadCount: () =>
		queryOptions({
			queryKey: ["notifications", "unread-count"],
			queryFn: () => client.getUnreadCount({}),
			refetchInterval: 30_000,
		}),
};
