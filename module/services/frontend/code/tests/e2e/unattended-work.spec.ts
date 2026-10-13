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
		await page.goto("/admin/unattended-work");
		await page.waitForURL(
			(url) => url.pathname === "/admin/unattended-work",
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
		// The panel states the host's position on this schedule outright, and
		// since #1112 that position is "declared here, and for now fired here
		// too": nothing consumes the declaration, so the host runs the schedule
		// as a labelled stopgap. A page still claiming it fires nothing would be
		// the most misleading thing a schedule panel can say.
		await expect(
			page.getByText(
				/this host still fires the schedule itself, as a\s+stopgap/i,
			),
		).toBeVisible();
		// And it says that a person's "Sync now" is the same admission, which is
		// why the two sit in one table rather than in two lists.
		await expect(
			page.getByText(/admits the same operation with an effect identity/),
		).toBeVisible();
	});

	test("a person's pass is listed beside the scheduled occurrences, with its effect identity", async ({
		page,
	}) => {
		await page.goto("/admin/datasources");
		await page.waitForURL((url) => url.pathname === "/admin/datasources", {
			timeout: 15000,
		});
		await expect(
			page.getByRole("heading", { name: "Reconcile schedules" }),
		).toBeVisible({ timeout: 20000 });

		// Either this organization has a connected source or it does not; both
		// are real answers and the panel gives one of them.
		const row = page.locator('[data-slot="source-trigger"]').first();
		const empty = page.getByText("No connected source declares a schedule");
		await expect(row.or(empty).first()).toBeVisible({ timeout: 20000 });
		if (await empty.isVisible()) return;

		// The admissions table, with the declared schedule's two occurrences in
		// it before anything is pressed.
		const admissions = row.locator('[data-slot="admissions"]');
		await expect(admissions).toBeVisible();
		await expect(
			admissions.locator('[data-slot="scheduled-occurrence"]'),
		).toHaveCount(2);
		await expect(admissions.locator('[data-slot="manual-pass"]')).toHaveCount(
			0,
		);

		// "Sync now" is the kit panel's control, above this one. Pressing it
		// admits the host's reconcile operation, and the pass lands in THIS
		// table — the same table as the occurrences, which is the whole claim.
		const sync = page.getByRole("button", { name: /^Sync$/ }).first();
		if ((await sync.count()) === 0) return;
		await sync.click();
		const manual = admissions.locator('[data-slot="manual-pass"]').first();
		await expect(manual).toBeVisible({ timeout: 20000 });
		// Not blank: the effect identity is what makes it the same object a
		// scheduled occurrence is, so the row would be meaningless without it.
		await expect(manual).toContainText(/[0-9a-f-]{8,}/);
	});
});
