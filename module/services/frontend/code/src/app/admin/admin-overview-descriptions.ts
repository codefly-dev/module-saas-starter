import { FRONTEND_NAVIGATION } from "@/gen/saas/frontend/v1/plugin_catalog";

/**
 * What each Admin card opens, in a person's words. The overview's cards said
 * "Open users.", "Open api keys." — the label again, which tells an
 * administrator nothing about where to go. Keyed by the catalog's navigation
 * id; a test holds every card the catalog offers on this page to a sentence
 * here, so a new entry cannot fall back to filler silently.
 */
export const ADMIN_CARD_DESCRIPTIONS: Readonly<Record<string, string>> = {
	"admin-users": "The people who can sign in, and their roles.",
	"admin-organizations": "Your organizations, their members and their roles.",
	"admin-teams":
		"Group people; give a team a role once and every member holds it.",
	"admin-roles": "Organization roles and the permissions each one grants.",
	"admin-permissions": "Every permission an organization role can grant.",
	"admin-invitations": "Invite people by e-mail and follow pending invites.",
	"admin-api-keys": "Keys that let a program call the API for you.",
	"admin-organization-settings":
		"Name, branding and custom domain; leave the organization.",
	"admin-platform-users": "Platform administrators and their roles.",
	"admin-platform-organizations": "Every organization on the platform.",
	"admin-feature-flags": "Turn product features on or off.",
	"admin-waitlist": "People waiting for access.",
	"admin-sessions": "Signed-in sessions, and ending one.",
	"admin-entitlements": "What your plan includes and how much is used.",
	"admin-job-operations": "Background jobs and their outcomes.",
	"admin-catalogue": "What the platform offers to install.",
	"admin-audit-log": "Who did what, and when.",
	"admin-module-authority": "What each module may do on its own.",
	"admin-event-operations": "Platform events and their delivery.",
	"admin-plugin-transport": "How pages reach the services behind them.",
	"admin-unattended-work": "Work that runs without a person present.",
	"admin-webhooks": "Send events to your own systems as they happen.",
	"admin-solutions":
		"Install solutions, and give each role to the people who need it.",
	"admin-modules": "The platform's modules, their releases and health.",
	"admin-datasources": "Connect the systems your documents come from.",
	"api-docs": "Reference for every API operation.",
	sdks: "Client libraries for calling the API.",
	compliance: "How the platform handles data and security.",
	settings: "Your own profile and preferences.",
	security: "Your password, two-factor sign-in and sessions.",
	notifications: "Which notifications you receive, and where.",
	"data-privacy": "Export your data or delete your account.",
	"privacy-choices": "Optional analytics and marketing choices.",
};

/** The ids of the catalog entries the Admin overview can show as cards. */
export const ADMIN_OVERVIEW_IDS = FRONTEND_NAVIGATION.filter(
	(item) => item.group && item.surfaces.includes("plugin_registry"),
).map((item) => item.id);
