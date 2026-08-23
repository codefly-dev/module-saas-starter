// Anti-clickjacking + CSP regression (issue #210). Proves the product
// frontend ships the framing/CSP defenses on real responses — both the
// public login page and an authenticated dashboard route — so the
// logged-in dashboard cannot be framed for a clickjacking attack.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

function assertHardened(response: Awaited<ReturnType<Page["goto"]>>) {
	expect(response, "navigation returned no response").not.toBeNull();
	const headers = response?.headers() ?? {};
	expect(headers["x-frame-options"]).toBe("DENY");
	expect(headers["x-content-type-options"]).toBe("nosniff");
	const csp = headers["content-security-policy"];
	expect(csp).toBeDefined();
	expect(csp).toContain("frame-ancestors 'none'");
}

test.describe("Security headers", () => {
	test("public responses are not framable and carry a CSP", async ({
		page,
	}) => {
		const response = await page.goto("/auth/login");
		assertHardened(response);
	});

	test("authenticated dashboard responses are not framable", async ({
		page,
	}) => {
		await page.goto("/auth/login");
		await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
		await page.getByText("Sarah Chen").click();
		await expect(page.getByText("Welcome back")).toBeVisible({
			timeout: 20000,
		});
		await resolveConsentPrompt(page);

		const response = await page.goto("/");
		assertHardened(response);
	});
});
