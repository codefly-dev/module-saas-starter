// Anti-clickjacking + CSP regression (issue #210). Proves the product
// frontend ships the framing/CSP defenses on real responses — both the
// public login page and an authenticated dashboard route — so the
// logged-in dashboard cannot be framed for a clickjacking attack.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

function cspOf(response: Awaited<ReturnType<Page["goto"]>>): string {
	expect(response, "navigation returned no response").not.toBeNull();
	const headers = response?.headers() ?? {};
	expect(headers["x-frame-options"]).toBe("DENY");
	expect(headers["x-content-type-options"]).toBe("nosniff");
	const csp = headers["content-security-policy"];
	expect(csp, "response is missing a CSP").toBeDefined();
	return csp as string;
}

function scriptSrc(csp: string): string {
	const found = csp
		.split(";")
		.map((part) => part.trim())
		.find((part) => part.startsWith("script-src "));
	expect(found, `no script-src in CSP: ${csp}`).toBeDefined();
	return found as string;
}

function assertHardened(response: Awaited<ReturnType<Page["goto"]>>) {
	const csp = cspOf(response);
	expect(csp).toContain("frame-ancestors 'none'");
	// The nonce-based CSP is the XSS mitigation: no 'unsafe-inline', a real
	// per-request nonce, and 'strict-dynamic'. An injected inline <script>
	// without this nonce is refused by the browser.
	const script = scriptSrc(csp);
	expect(script).not.toContain("'unsafe-inline'");
	expect(script).toMatch(/'nonce-[A-Za-z0-9+/=]+'/);
	expect(script).toContain("'strict-dynamic'");
}

test.describe("Security headers", () => {
	test("public responses are not framable and carry a nonce'd CSP", async ({
		page,
	}) => {
		const response = await page.goto("/auth/login");
		assertHardened(response);
	});

	test("the CSP nonce is minted per request", async ({ page }) => {
		const nonceOf = (csp: string) =>
			scriptSrc(csp).match(/'nonce-([A-Za-z0-9+/=]+)'/)?.[1];
		const first = nonceOf(cspOf(await page.goto("/auth/login")));
		const second = nonceOf(cspOf(await page.goto("/auth/login")));
		expect(first).toBeDefined();
		expect(second).toBeDefined();
		expect(first).not.toBe(second);
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
