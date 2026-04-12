// TODO: Replace with real Connect RPC client when NotificationService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { NotificationService } from "@/gen/user_pb";
// const client = createClient(NotificationService, apiTransport);

export const notificationMutations = {
  markRead: async (notificationId: string) => {
    // TODO: Replace with real RPC call
    // return client.markRead({ notificationId });
    return { notificationId };
  },

  markAllRead: async () => {
    // TODO: Replace with real RPC call
    // return client.markAllRead({});
    return {};
  },

  delete: async (notificationId: string) => {
    // TODO: Replace with real RPC call
    // return client.deleteNotification({ notificationId });
    return {};
  },
};
