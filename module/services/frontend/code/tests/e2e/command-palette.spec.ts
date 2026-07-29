// Cmd-K command palette regression. Three things matter:
//   1. The cmd+K hotkey opens it (Linear/GitHub/Slack convention).
//   2. The nav list is gated by role — Bob doesn't see admin entries.
//   3. Selecting an entry navigates to the right URL.
//
// We don't exercise the user-search code path here because it requires
// a debounced async RPC and the existing auth-boundary spec already
// proves SearchUsers is platform-admin-gated server-side.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAs(page: Page, fixtureName: string) {
	await page.goto("/auth/login");
	await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
	await page.getByText(fixtureName).click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test.describe("Command palette", () => {
	test("cmd+K opens it for an authenticated user and ESC closes", async ({
		page,
	}) => {
		await loginAs(page, "Sarah Chen");
		// After login the route transitions to / and CommandPalette mounts.
		// The window-level keydown listener is attached in a useEffect, so
		// it lands AFTER the next paint. Wait for the sidebar's nav to
		// render — same React tree as the palette, proves the layout
		// mounted and effects ran. (waitForLoadState networkidle doesn't
		// work here: the notifications SSE stream keeps the network
		// permanently active.)
		await expect(page.getByRole("link", { name: "Dashboard" })).toBeVisible();
		// Click body so the page (not the URL bar) has keyboard focus.
		await page.locator("body").click();
		await page.keyboard.press("Control+K");
		await expect(page.getByPlaceholder("Search or jump to...")).toBeVisible();
		await page.keyboard.press("Escape");
		await expect(page.getByPlaceholder("Search or jump to...")).toHaveCount(0);
	});

	test("super_admin sees all admin nav entries", async ({ page }) => {
		await loginAs(page, "Sarah Chen");
		await page.keyboard.press("Control+K");
		// Spot-check a super_admin-only entry and a regular admin one.
		// exact: false — cmdk renders the label inside a div with icon.
		await expect(
			page.getByRole("option", { name: /Platform Users/i }),
		).toBeVisible();
		await expect(
			page.getByRole("option", { name: /^Webhooks/i }),
		).toBeVisible();
	});

	test("regular member does not see administration entries", async ({
		page,
	}) => {
		await loginAs(page, "Bob Williams");
		await page.keyboard.press("Control+K");
		await expect(page.getByPlaceholder("Search or jump to...")).toBeVisible();

		// Admin entries must be hidden. RoleGate semantics: if Bob ever
		// dangled "Webhooks" in the palette, clicking it would 403 server-
		// side, but the UX would be broken.
		await expect(page.getByRole("option", { name: /^Webhooks/i })).toHaveCount(
			0,
		);
		await expect(
			page.getByRole("option", { name: /Platform Users/i }),
		).toHaveCount(0);
	});

	test("selecting an entry navigates to its href", async ({ page }) => {
		await loginAs(page, "Sarah Chen");
		await page.keyboard.press("Control+K");
		await page.getByRole("option", { name: /^Webhooks/i }).click();
		await page.waitForURL(/\/admin\/webhooks/);
	});
});
