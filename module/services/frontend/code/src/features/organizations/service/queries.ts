import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { OrganizationService } from "@/gen/saas-starter_api_grpc_pb";

const client = createClient(OrganizationService, apiTransport);

export const orgQueries = {
  list: () =>
    queryOptions({
      queryKey: ["organizations"],
      queryFn: () => client.listOrganizations({}),
    }),

  detail: (id: string) =>
    queryOptions({
      queryKey: ["organizations", id],
      queryFn: () => client.getOrganization({ id }),
      enabled: !!id,
    }),

  members: (orgId: string) =>
    queryOptions({
      queryKey: ["org-members", orgId],
      queryFn: () => client.listMembers({ orgId }),
      enabled: !!orgId,
    }),

  settings: (orgId: string) =>
    queryOptions({
      queryKey: ["org-settings", orgId],
      queryFn: () => client.getOrgSettings({ orgId }),
      enabled: !!orgId,
    }),
};
