// SSO admin E2E. Accounts runs in stub mode
// (IDENTITY_MANAGEMENT_API_KEY absent from the fixture identity configuration),
// so StartSetup returns a placeholder URL pointing
// at /admin/sso?demo=1. We can verify the full state-machine —
// unconfigured → linked → disabled — without real WorkOS credentials.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
	await expect
		.poll(async () =>
			(await page.context().cookies()).some(
				(item) => item.name === "codefly_rt" && item.value.length > 0,
			),
		)
		.toBe(true);
}

async function pickAcmeOrg(page: Page) {
	const trigger = page.getByRole("combobox").first();
	await trigger.click();
	await page
		.getByRole("option", { name: /acme corp/i })
		.click({ timeout: 15_000 });
}

async function ensureInactiveSSO(page: Page) {
	const disable = page.getByRole("button", { name: /disable sso/i });
	if (await disable.isVisible()) {
		page.once("dialog", (dialog) => dialog.accept());
		await disable.click();
		await expect(page.getByText(/sso disabled/i)).toBeVisible({
			timeout: 10_000,
		});
	}
	await expect(
		page.getByRole("button", { name: /^(set up|re-enable) sso$/i }),
	).toBeVisible();
}

test.describe("SSO admin page", () => {
	test.beforeEach(async ({ page }) => {
		await loginAsSuperAdmin(page);
		await page.goto("/admin/sso");
		await expect(
			page.getByRole("heading", { name: /single sign-on/i }),
		).toBeVisible();
	});

	test("the authenticated organization shows its SSO state", async ({ page }) => {
		await expect(
			page.getByText(/not configured|setup pending|active|disabled/i).first(),
		).toBeVisible();
	});

	test("inactive org shows an SSO setup action", async ({ page }) => {
		await pickAcmeOrg(page);
		await ensureInactiveSSO(page);
	});

	test("StartSetup transitions the org to linked + Disable clears it", async ({
		page,
	}) => {
		await pickAcmeOrg(page);
		await ensureInactiveSSO(page);

		// Stub mode (no identity management API key): StartSetup persists status=
		// "linked" and returns demo URL. The page redirects there; we
		// wait for the exact demo URL. The mutation only redirects after
		// StartSetup has returned, so this also proves the linked row was
		// committed before we navigate back and re-query it.
		const startBtn = page.getByRole("button", {
			name: /^(set up|re-enable) sso$/i,
		});
		await Promise.all([
			page.waitForURL(/\/admin\/sso\?demo=1$/),
			startBtn.click(),
		]);
		// After the redirect to demo URL, force-navigate back.
		await page.goto("/admin/sso");
		await pickAcmeOrg(page);

		// Now the row should be in "linked" state.
		await expect(page.getByText(/setup pending/i)).toBeVisible({
			timeout: 10_000,
		});

		// Disable flow.
		page.once("dialog", (d) => d.accept());
		await page.getByRole("button", { name: /disable sso/i }).click();
		await expect(page.getByText(/sso disabled/i)).toBeVisible({
			timeout: 10_000,
		});

		// Status flips to Disabled, "Re-enable SSO" CTA appears.
		await expect(page.getByText(/^disabled$/i).first()).toBeVisible();
		await expect(
			page.getByRole("button", { name: /re-enable sso/i }),
		).toBeVisible();
	});
});
