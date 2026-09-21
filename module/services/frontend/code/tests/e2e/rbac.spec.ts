// The RBAC administration journey end to end: mint a role, grant it to a
// person, confirm the grant on the permissions browser, then take it away from
// that person's own page. Each step reads the surface an administrator reads
// rather than the API, so a page that renders but never wires its mutation
// fails here.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

const ROLE_NAME = `e2e-rbac-${Date.now()}`;

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test.describe("RBAC administration", () => {
	test("grant a role, see it in the permissions browser, revoke it from the user", async ({
		page,
	}) => {
		await loginAsSuperAdmin(page);

		// 1. Mint a role carrying one permission.
		await page.goto("/admin/roles");
		await expect(page.getByRole("heading", { name: /^Roles$/ })).toBeVisible();
		await page.getByRole("button", { name: /create role/i }).click();
		await page.getByLabel(/name/i).first().fill(ROLE_NAME);
		await page.getByRole("button", { name: /^create$/i }).click();
		const roleLink = page.getByRole("link", { name: ROLE_NAME });
		await expect(roleLink).toBeVisible({ timeout: 15000 });

		// 2. The role's own page edits its permission set in place, which is
		//    what UpdateRole added — delete-and-recreate would lose grants.
		await roleLink.click();
		await page.waitForURL(/\/admin\/roles\/[^/]+$/, { timeout: 15000 });
		await expect(page.getByRole("heading", { name: ROLE_NAME })).toBeVisible();
		await page.getByLabel("users:read").check();
		await page.getByRole("button", { name: /save changes/i }).click();
		await expect(page.getByText(/updated/i).first()).toBeVisible({
			timeout: 15000,
		});

		// 3. Grant it to a person from their own page.
		await page.goto("/admin/users");
		const userLink = page.getByRole("link", { name: /@/ }).first();
		await userLink.click();
		await page.waitForURL(/\/admin\/users\/[^/]+$/, { timeout: 15000 });
		await page.getByRole("button", { name: /manage roles/i }).click();
		await page.getByRole("combobox").first().click();
		await page.getByRole("option", { name: ROLE_NAME }).click();
		await page.getByRole("button", { name: /^assign$/i }).click();
		await expect(page.getByText(/role assigned/i)).toBeVisible({
			timeout: 15000,
		});
		await page.getByRole("button", { name: /^done$/i }).click();

		// The effective-permissions table is the "and why" half: the permission
		// is there because that role carries it.
		await expect(page.getByText("users:read").first()).toBeVisible({
			timeout: 15000,
		});
		await expect(page.getByText(ROLE_NAME).first()).toBeVisible();

		// 4. The permissions browser answers the reverse question.
		await page.goto("/admin/permissions");
		await expect(
			page.getByRole("heading", { name: /^Permissions$/ }),
		).toBeVisible();
		await expect(page.getByText(ROLE_NAME).first()).toBeVisible({
			timeout: 15000,
		});

		// 5. Revoke from the user page, and watch the permission leave with it.
		await page.goBack();
		await page.waitForURL(/\/admin\/users\/[^/]+$/, { timeout: 15000 });
		await page.getByRole("button", { name: "Revoke" }).first().click();
		await expect(page.getByText(/role revoked/i)).toBeVisible({
			timeout: 15000,
		});
		await expect(page.getByText(ROLE_NAME)).toHaveCount(0);
	});
});
