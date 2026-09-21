/** Explorer-only Connect transport. No request reaches a real backend. */
import { createConnectTransport } from "@connectrpc/connect-web";

const date = "2026-09-01T12:00:00Z";
const reads: Record<string, unknown> = {
	SearchUsers: {
		users: [
			{
				uuid: "00000000-0000-4000-8000-000000000001",
				primaryEmail: "jane@example.com",
				status: 1,
				emailVerified: true,
				profile: { display_name: "Jane Doe" },
				createdAt: date,
			},
		],
	},
	ListOrganizations: {
		organizations: [
			{
				id: "example-org",
				name: "Acme Workspace",
				slug: "acme",
				createdAt: date,
			},
		],
	},
	ListTeams: {
		teams: [
			{
				id: "example-team",
				orgId: "example-org",
				name: "Engineering",
				description: "Product engineering team",
				createdAt: date,
			},
		],
	},
	ListTeamMembers: { members: [] },
	ListRoles: {
		roles: [
			{
				id: "example-role",
				name: "Project editor",
				description: "Manage project content",
				permissions: [{ resource: "documents", action: "read" }],
			},
		],
	},
	ListAPIKeys: { keys: [] },
	ListInvitations: { invitations: [] },
	ListSubscriptions: { subscriptions: [] },
	ListDeliveries: { deliveries: [] },
	ListMembers: { members: [] },
	ListDevices: { devices: [] },
	GetSSO: {},
	ListPlatformAdmins: { admins: [] },
	ListFeatureFlags: { flags: [] },
	ListActiveSessions: { sessions: [] },
};
export const apiTransport = createConnectTransport({
	baseUrl: "https://preview.example.invalid",
	useBinaryFormat: false,
	fetch: async (input) => {
		const url = new URL(
			typeof input === "string"
				? input
				: input instanceof URL
					? input.href
					: input.url,
		);
		const method = url.pathname.split("/").at(-1) ?? "";
		if (method in reads) return Response.json(reads[method]);
		const response = method.startsWith("Create")
			? {
					id: "example-created",
					key: "example-preview-key",
					role: { id: "example-role", name: "Example role" },
					invitation: { id: "example-invitation" },
				}
			: {};
		if (/^(Create|Update|Delete|Revoke|Resend|Assign)/.test(method))
			return Response.json(response);
		return Response.json(
			{
				code: "unimplemented",
				message: `No explorer fixture for ${url.pathname}`,
			},
			{ status: 501 },
		);
	},
});
