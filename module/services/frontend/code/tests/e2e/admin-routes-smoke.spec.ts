// Smoke coverage for the admin routes that had no direct Playwright
// assertion: each renders its own surface end-to-end (frontend → gateway →
// api) for a super_admin, without bouncing back to login or tripping the
// super-admin gate. Complements navigation.spec.ts, which exercises the
// sidebar links rather than the destination content.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

interface AdminRoute {
	path: string;
	// A heading unique to the destination, asserted when the surface exposes a
	// stable one. Omitted where the heading is dynamic — those routes fall back
	// to the generic "a heading rendered, no login bounce" smoke.
	heading?: RegExp;
	// Routes wrapped in <RoleGate require="super_admin"> must NOT show the gate
	// fallback for Sarah (a super_admin).
	platformGated?: boolean;
}

const ROUTES: AdminRoute[] = [
	{ path: "/admin/roles", heading: /^Roles$/ },
	{ path: "/admin/teams", heading: /^Teams$/ },
	{ path: "/admin/organizations", heading: /^Organizations$/ },
	{ path: "/admin/invitations", heading: /^Invitations$/ },
	{ path: "/admin/api-keys", heading: /^API Keys$/ },
	{ path: "/admin/entitlements", heading: /^Entitlements$/ },
	{ path: "/admin/sessions", heading: /Active Sessions/ },
	{ path: "/admin/platform", platformGated: true },
	{
		path: "/admin/platform/waitlist",
		heading: /Waitlist/,
		platformGated: true,
	},
	{ path: "/admin/platform/feature-flags", platformGated: true },
	{
		path: "/admin/platform/admins",
		heading: /Platform Admins/,
		platformGated: true,
	},
	{
		path: "/admin/platform/jobs",
		heading: /Job operations/i,
		platformGated: true,
	},
];

test.describe("admin route smoke", () => {
	test.beforeEach(async ({ page }) => {
		await loginAsSuperAdmin(page);
	});

	for (const route of ROUTES) {
		test(`${route.path} renders its admin surface`, async ({ page }) => {
			await page.goto(route.path);
			await page.waitForURL((url) => url.pathname === route.path, {
				timeout: 15000,
			});

			// A super_admin must never be bounced to login on an admin route.
			expect(page.url()).not.toContain("/auth/login");

			if (route.platformGated) {
				await expect(
					page.getByText("Super administrator required"),
				).toHaveCount(0);
			}

			if (route.heading) {
				await expect(
					page.getByRole("heading", { name: route.heading }).first(),
				).toBeVisible();
			} else {
				await expect(page.locator("h1, h2").first()).toBeVisible();
			}
		});
	}
});
