import { signInForScenario } from "./fixture-session";
// A platform administrator manages an organization by hand from the platform
// view, end to end (frontend → gateway → accounts): create it, rename it, add a
// member, change their role, remove them, and delete the organization. Uses the
// dev-admin fixture: Sarah Chen is a platform super_admin, carol@acme.com an
// ordinary user.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await signInForScenario(page, "Sarah Chen");
	await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test("a platform administrator manages an organization by hand", async ({
	page, browser,
}) => {
	const stamp = Date.now().toString(36);
	const name = `E2E Org ${stamp}`;
	const slug = `e2e-org-${stamp}`;

	await loginAsSuperAdmin(page);
	await page.goto("/admin/platform/organizations");
	await expect(
		page.getByRole("heading", { name: "Organizations" }),
	).toBeVisible();

	// Create.
	await page.getByRole("button", { name: "Create organization" }).click();
	const create = page.getByRole("dialog");
	await create.getByLabel("Name").fill(name);
	await expect(create.getByLabel("Slug")).toHaveValue(slug);
	await create.getByRole("button", { name: "Create" }).click();
	await expect(page.getByText(`Created ${name}`)).toBeVisible();

	// The creator is the owner. Enter that organization before changing it.
	const switcher = page.getByRole("button", { name: "Organization", exact: true });
	await switcher.click();
	await page.getByRole("menuitem", { name, exact: true }).click();
	await expect(switcher).toContainText(name);

	// Find it and open it.
	await page.getByLabel("Search organizations").fill(slug);
	await page.getByRole("button", { name }).click();
	const detail = page.getByRole("region", {
		name: new RegExp(`^Organization ${name}`),
	});
	await expect(detail).toBeVisible();

	// Rename.
	const renamed = `${name} renamed`;
	await detail.getByLabel("Name", { exact: true }).fill(renamed);
	await detail.getByRole("button", { name: "Save" }).click();
	await expect(page.getByText("Organization updated")).toBeVisible();

	// Add a member.
	await detail.getByPlaceholder("Search by name or email…").fill("carol");
	await detail.getByRole("option", { name: "carol@acme.com" }).click();
	await detail.getByRole("button", { name: "Add" }).click();
	await expect(page.getByText("Member added")).toBeVisible();

	// Change their role inline.
	await detail
		.getByRole("combobox", { name: "Role of carol@acme.com" })
		.click();
	await page.getByRole("option", { name: "Admin" }).click();
	await expect(page.getByText("Role updated")).toBeVisible();

	// Remove them.
	await detail.getByRole("button", { name: "Remove carol@acme.com" }).click();
	await page
		.getByRole("alertdialog")
		.getByRole("button", { name: "Remove" })
		.click();
	await expect(page.getByText("Member removed")).toBeVisible();

	// Delete the organization.
	await detail.getByRole("button", { name: "Delete", exact: true }).click();
	const confirm = page.getByRole("dialog");
	await confirm.getByLabel(/to confirm/).fill(slug);
	const deletion = page.waitForResponse(response => response.url().endsWith("/DeleteOrganization") && response.request().method() === "POST");
	await confirm.getByRole("button", { name: "Delete organization" }).click();
	const result = await deletion;
	expect(result.ok(), await result.text()).toBe(true);
	// The deleted membership no longer authorizes the page. Verify the
	// resulting organization state from a fresh platform directory session.
	const verifier = await browser.newPage({ baseURL: new URL(page.url()).origin });
	try {
		await loginAsSuperAdmin(verifier);
		await verifier.goto("/admin/platform/organizations");
		await verifier.getByLabel("Search organizations").fill(slug);
		await verifier.getByRole("switch").click();
		await expect(verifier.getByRole("row").filter({ hasText: renamed })).toContainText("Archived");
	} finally {
		await verifier.close();
	}
});
