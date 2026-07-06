import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { PlatformAdminService, UserService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(PlatformAdminService, apiTransport);
const userClient = createClient(UserService, apiTransport);

/** The profile fields we edit, folded into the User.profile map. Merged onto the
 * existing profile so unrelated keys are preserved (UpdateUser replaces the map).
 * primaryEmail is always sent — the User proto validates it as an email even for a
 * profile-only edit, so we echo the current address when it's unchanged. */
export interface UserEdit {
  uuid: string;
  profile: Record<string, string>;
  primaryEmail: string;
}

export const userMutations = {
  suspend: (userId: string, reason: string) =>
    client.suspendUser({ userId, reason }),

  unsuspend: (userId: string) =>
    client.unsuspendUser({ userId }),

  impersonate: (userId: string) =>
    client.impersonateUser({ userId }),

  // UpdateUser honors the target uuid with a self-or-admin gate; we send the merged
  // profile and the (current or edited) email as the User patch.
  update: ({ uuid, profile, primaryEmail }: UserEdit) =>
    userClient.updateUser({
      uuid,
      user: { uuid, profile, primaryEmail },
    }),

  // DeleteUser soft-deletes and revokes the user's sessions; takes a uuid identifier.
  remove: (uuid: string) =>
    userClient.deleteUser({ identifier: { case: "uuid", value: uuid } }),
};
