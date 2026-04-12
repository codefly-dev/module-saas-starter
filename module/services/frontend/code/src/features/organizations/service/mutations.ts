import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { OrganizationService } from "@/gen/user_pb";

const client = createClient(OrganizationService, apiTransport);

export const orgMutations = {
  create: (name: string, slug: string) =>
    client.createOrganization({ name, slug }),

  addMember: (orgId: string, userId: string, role: number) =>
    client.addMember({ orgId, userId, role }),

  removeMember: (orgId: string, userId: string) =>
    client.removeMember({ orgId, userId }),
};
