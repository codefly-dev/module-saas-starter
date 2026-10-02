import { createClient } from "@connectrpc/connect";
import { OrganizationService } from "@/gen/saas/accounts/v1/organizations_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(OrganizationService, apiTransport);

export const orgMutations = {
	// ownerUserId names someone else as the owner; the server honors it only
	// for a platform super administrator.
	create: (name: string, slug: string, ownerUserId = "") =>
		client.createOrganization({ name, slug, ownerUserId }),

	update: (orgId: string, name: string, slug: string) =>
		client.updateOrganization({ orgId, name, slug }),

	leave: (orgId: string) => client.leaveOrganization({ orgId }),

	delete: (orgId: string, confirmSlug: string) =>
		client.deleteOrganization({ orgId, confirmSlug }),

	// AddMember is an upsert: for an existing member it changes the role, under
	// the same last-administrator rule as a removal.
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
