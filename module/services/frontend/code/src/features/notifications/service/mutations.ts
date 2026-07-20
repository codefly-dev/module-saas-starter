import { createClient } from "@connectrpc/connect";
import { NotificationService } from "@/gen/saas/accounts/v1/notifications_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(NotificationService, apiTransport);

export const notificationMutations = {
	markRead: (id: string) => client.markRead({ id }),

	markAllRead: () => client.markAllRead({}),

	delete: (id: string) => client.deleteNotification({ id }),
};
