import { createClient } from "@connectrpc/connect";
import { OrganizationService } from "@/gen/saas/accounts/v1/organizations_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(OrganizationService, apiTransport);

export const orgMutations = {
	create: (name: string, slug: string) =>
		client.createOrganization({ name, slug }),

	addMember: (orgId: string, userId: string, role: number) =>
		client.addMember({ orgId, userId, role }),

	removeMember: (orgId: string, userId: string) =>
		client.removeMember({ orgId, userId }),

	updateSettings: (
		orgId: string,
		settings: {
			logoUrl: string;
			primaryColor: string;
			customDomain: string;
			faviconUrl: string;
		},
	) =>
		client.updateOrgSettings({
			orgId,
			logoUrl: settings.logoUrl,
			primaryColor: settings.primaryColor,
			customDomain: settings.customDomain,
			faviconUrl: settings.faviconUrl,
		}),
};
