import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { PlatformAdminService } from "@/gen/user_pb";

const client = createClient(PlatformAdminService, apiTransport);

export const userMutations = {
  suspend: (userId: string, reason: string) =>
    client.suspendUser({ userId, reason }),

  unsuspend: (userId: string) =>
    client.unsuspendUser({ userId }),

  impersonate: (userId: string) =>
    client.impersonateUser({ userId }),
};
