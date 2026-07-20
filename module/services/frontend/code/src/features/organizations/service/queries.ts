import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { OrganizationService } from "@/gen/saas/accounts/v1/organizations_pb";
import { apiTransport } from "@/lib/connect/transport";

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
