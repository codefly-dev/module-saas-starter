import { signInForScenario } from "./fixture-session";
// The Unattended work page, in a real browser against the real host.
//
// What this proves that the container test cannot: the page's reads reach the
// host through the gateway and come back as the host actually answers them —
// the schedule a source's interval produces, and whose access its runs use.
// The container test drives the page against recorded answers; this one asks
// the live host what it declares.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await signInForScenario(page, "Sarah Chen");
	await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
}

test.describe("unattended work", () => {
	test.beforeEach(async ({ page }) => {
		await loginAsSuperAdmin(page);
	});

	test("the page states what runs unattended and whose access it would use", async ({
		page,
	}) => {
		await page.goto("/admin/platform/unattended-work");
		await page.waitForURL((url) => url.pathname === "/admin/platform/unattended-work", {
			timeout: 15000,
		});
		expect(page.url()).not.toContain("/auth/login");

		// The super-admin gate must not refuse a super administrator.
		await expect(
			page.getByRole("heading", { name: "Unattended work" }),
		).toBeVisible({ timeout: 20000 });
		await expect(page.getByText("Super administrator required")).toHaveCount(0);

		// Either there is a source whose unattended work can be shown, or the
		// page says there is not. Both are real answers and the page must give
		// one of them rather than an empty region, which is the whole point of
		// the read-outcome rule this surface follows.
		const empty = page.getByText("No data source is connected");
		const picker = page.getByRole("combobox", { name: "Data source" });
		await expect(empty.or(picker).first()).toBeVisible({ timeout: 20000 });
		if (await empty.isVisible()) return;

		// With a source selected, the page answers in an administrator's words:
		// a schedule (or that there is none), and whose access its runs use.
		await expect(
			page.getByRole("heading", { name: "Schedule", exact: true }),
		).toBeVisible({ timeout: 20000 });
		await expect(
			page.getByRole("heading", { name: "Whose access it runs with" }),
		).toBeVisible();
		await expect(
			page.getByRole("heading", { name: "Recent runs" }),
		).toBeVisible();
		// Never blank: a schedule naming nobody's access says so, because a run
		// under it would be refused; a source with no schedule says that.
		await expect(
			page
				.getByText(
					/A person's access, listed below|The module's own access|Nobody's access, so runs are refused|This source has no schedule/,
				)
				.first(),
		).toBeVisible({ timeout: 20000 });

		// The platform's own names for the same facts stay available to an
		// engineer, one press away: the release and the operation a run admits.
		await page.getByRole("button", { name: "Technical details" }).click();
		await expect(page.getByText("saas_datasource_reconcile")).toBeVisible({
			timeout: 20000,
		});
		await expect(
			page.getByText("/saas.accounts.v1.DatasourceService/ReconcileSource"),
		).toBeVisible();
	});

	test("a source's advanced settings explain its sync schedule", async ({ page }) => {
		await page.goto("/admin/datasources");
		await page.getByRole("button", { name: "Advanced" }).click();
		await expect(page.getByRole("heading", { name: "Sync schedule" })).toBeVisible();
		const schedule = page.getByRole("list", { name: "Sync schedule" });
		const empty = page.getByText("No source is connected yet.", { exact: true });
		await expect(schedule.or(empty)).toBeVisible();
		if (await schedule.isVisible()) {
			await expect(schedule.getByRole("listitem").first()).toContainText(/Syncs every|Syncs when you press Sync now/);
		}
	});

	test("a manual run requires a connected source with active access", async ({ page }) => {
		await page.goto("/admin/platform/unattended-work");
		const empty = page.getByRole("heading", { name: "No data source is connected" });
		const run = page.getByRole("button", { name: "Run now" });
		await expect(empty.or(run)).toBeVisible();
		if (await empty.isVisible()) {
			await expect(run).toHaveCount(0);
			return;
		}
		await expect(page.getByRole("heading", { name: "Whose access it runs with" })).toBeVisible();
		if (await run.isDisabled()) {
			await expect(run).toHaveAttribute("title", /Set up someone's access first/);
			return;
		}
		const admission = page.waitForResponse(response =>
			response.request().method() === "POST" && response.url().endsWith("/ReconcileSource")
		);
		await run.click();
		const response = await admission;
		expect(response.ok(), await response.text()).toBe(true);
		expect((await response.json()).effectId).toBeTruthy();
		await expect(page.getByText(/Run started for/)).toBeVisible();
	});
});
