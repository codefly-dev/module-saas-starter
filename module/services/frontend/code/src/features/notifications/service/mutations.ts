import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { NotificationService } from "@/gen/user_pb";

const client = createClient(NotificationService, apiTransport);

export const notificationMutations = {
  markRead: (id: string) =>
    client.markRead({ id }),

  markAllRead: () =>
    client.markAllRead({}),

  delete: (id: string) =>
    client.deleteNotification({ id }),
};
