import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";

// Presentation only: the service still supplies the available activities and
// the exact resource/action pairs submitted to it. These labels grant nothing.
const labels: Record<string, string> = {
	[PERMISSIONS.ALL]: "Manage everything",
	[PERMISSIONS.API_KEYS_READ]: "View API keys",
	[PERMISSIONS.API_KEYS_WRITE]: "Create and revoke API keys",
	[PERMISSIONS.AUDIT_READ]: "View and export the activity log",
	[PERMISSIONS.BILLING_READ]: "View billing and invoices",
	[PERMISSIONS.BILLING_WRITE]: "Manage subscriptions and payment details",
	[PERMISSIONS.DASHBOARDS_READ]: "View personal and shared dashboards",
	[PERMISSIONS.DASHBOARDS_SHARE]: "Share dashboards with the organization",
	[PERMISSIONS.DASHBOARDS_WRITE]: "Create and edit personal dashboards",
	[PERMISSIONS.DATASOURCE_INVOKE]: "Run the actions a data source provides",
	[PERMISSIONS.DATASOURCE_READ]: "View the actions a data source provides",
	[PERMISSIONS.ENTITLEMENTS_READ]: "View plan limits and usage",
	[PERMISSIONS.INVITATIONS_READ]: "View invitations",
	[PERMISSIONS.INVITATIONS_WRITE]: "Invite people and cancel invitations",
	[PERMISSIONS.KNOWLEDGE_READ]: "Read shared content",
	[PERMISSIONS.KNOWLEDGE_WRITE]: "Create and edit shared content",
	[PERMISSIONS.MODULE_ENVELOPE_READ]:
		"View the maximum access allowed for modules",
	[PERMISSIONS.MODULE_ENVELOPE_WRITE]:
		"Change the maximum access allowed for modules",
	[PERMISSIONS.ORGS_READ]: "View organization details",
	[PERMISSIONS.ORGS_WRITE]: "Manage organization settings and members",
	[PERMISSIONS.ROLES_READ]: "View roles and who has them",
	[PERMISSIONS.ROLES_WRITE]: "Create roles and manage role assignments",
	[PERMISSIONS.TEAMS_READ]: "View teams and their members",
	[PERMISSIONS.TEAMS_WRITE]: "Manage teams and their members",
	[PERMISSIONS.USERS_READ]: "View people",
	[PERMISSIONS.USERS_WRITE]: "Create, update, and delete user accounts",
	[PERMISSIONS.WEBHOOKS_READ]: "View webhooks and delivery history",
	[PERMISSIONS.WEBHOOKS_WRITE]:
		"Manage webhooks, retry deliveries, and replace secrets",
};

export function activityLabel(activity: {
	resource: string;
	action: string;
	description?: string;
}): string | undefined {
	return (
		labels[`${activity.resource}:${activity.action}`] ||
		activity.description?.trim() ||
		undefined
	);
}
