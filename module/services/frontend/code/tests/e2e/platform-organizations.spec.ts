// A platform administrator manages an organization by hand from the platform
// view, end to end (frontend → gateway → accounts): create it, rename it, add a
// member, change their role, remove them, and delete the organization. Uses the
// dev-admin fixture: Sarah Chen is a platform super_admin, carol@acme.com an
// ordinary user.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test("a platform administrator manages an organization by hand", async ({
	page,
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
	await detail.getByRole("button", { name: "carol@acme.com" }).click();
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
	await confirm.getByRole("button", { name: "Delete organization" }).click();
	await expect(page.getByText(`${renamed} was deleted`)).toBeVisible();
});
