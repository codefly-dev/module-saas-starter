// The unattended-work playground, in a real browser against the real host.
//
// What this proves that the container test cannot: the page's reads reach the
// host through the gateway and come back as the host actually answers them —
// the declaration a source's interval produces, and whether any reference is
// bound to it. The container test drives the walk against recorded answers;
// this one asks the live host what it declares.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test.describe("unattended work", () => {
	test.beforeEach(async ({ page }) => {
		await loginAsSuperAdmin(page);
	});

	test("the playground states whose authority an occurrence would run under", async ({
		page,
	}) => {
		await page.goto("/admin/platform/unattended-work");
		await page.waitForURL(
			(url) => url.pathname === "/admin/platform/unattended-work",
			{ timeout: 15000 },
		);
		expect(page.url()).not.toContain("/auth/login");

		// The super-admin gate must not refuse a super administrator.
		await expect(
			page.getByRole("heading", { name: "Unattended work" }),
		).toBeVisible({ timeout: 20000 });
		await expect(page.getByText("Super administrator required")).toHaveCount(0);

		// Either there is a source to bind an identity to, or the page says
		// there is not. Both are real answers and the page must give one of
		// them rather than an empty region — which is the whole point of the
		// read-outcome rule this surface follows.
		const empty = page.getByText(
			"Connect a data source to bind a trigger identity",
		);
		const picker = page.getByRole("combobox", { name: "Data source" });
		await expect(empty.or(picker).first()).toBeVisible({ timeout: 20000 });
		if (await empty.isVisible()) return;

		// With a source selected, the declaration is on screen: the release, the
		// operation an occurrence admits, and which arm its identity names.
		await expect(page.getByText("saas_datasource_reconcile")).toBeVisible({
			timeout: 20000,
		});
		await expect(
			page.getByText("/saas.accounts.v1.DatasourceService/ReconcileSource"),
		).toBeVisible();
		// Never blank: a declaration naming no identity says so, because an
		// occurrence under it would be refused.
		await expect(
			page
				.getByText(
					/A delegation reference|An installation binding|None — occurrences would be refused/,
				)
				.first(),
		).toBeVisible();
	});

	test("a source's settings show the reconcile schedule as the trigger it declares", async ({
		page,
	}) => {
		await page.goto("/admin/datasources");
		await page.waitForURL((url) => url.pathname === "/admin/datasources", {
			timeout: 15000,
		});
		await expect(
			page.getByRole("heading", { name: "Reconcile schedules" }),
		).toBeVisible({ timeout: 20000 });
		// The panel states the host's position on this schedule outright: it
		// declares the trigger and fires nothing on it.
		await expect(
			page.getByText("This host fires nothing on it."),
		).toBeVisible();
	});
});
