import { createClient } from "@connectrpc/connect";
import { NotificationService } from "@/gen/saas/accounts/v1/notifications_pb";
import { apiTransport } from "@/lib/connect/transport";
import { notificationActionUrl } from "../model/transforms";

const client = createClient(NotificationService, apiTransport);

export const notificationMutations = {
	markRead: (id: string) => client.markRead({ id }),

	markAllRead: () => client.markAllRead({}),

	delete: (id: string) => client.deleteNotification({ id }),

	/**
	 * Resolves where a notification points, rechecking access as it is followed.
	 * Rejects with a NOT_FOUND ConnectError when the caller may no longer reach
	 * the resource. The shape check still runs on the answer: the server settles
	 * authority, not whether the stored string is a usable local path.
	 *
	 * A rejected shape throws rather than resolving undefined — this feeds a
	 * navigation, and a caller that cannot navigate has to be told, not left with
	 * a click that silently does nothing.
	 */
	resolveAction: async (id: string): Promise<string> => {
		const { actionUrl } = await client.resolveNotificationAction({ id });
		const local = notificationActionUrl(actionUrl);
		if (!local) {
			throw new Error("notification destination is not a local path");
		}
		return local;
	},
};
