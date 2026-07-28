import { expect, test } from "@playwright/test";

test.describe("Acquisition journey", () => {
	test("legal links resolve to explicit starter content", async ({ page }) => {
		await page.goto("/legal/terms");
		await expect(
			page.getByRole("heading", { name: "Terms of Service" }),
		).toBeVisible();

		await page.goto("/legal/privacy");
		await expect(
			page.getByRole("heading", { name: "Privacy Policy" }),
		).toBeVisible();
		await expect(
			page.getByText(/technical placeholder|Legal content is not configured/),
		).toBeVisible();
	});

	test("public waitlist truthfully reflects the configured acquisition mode", async ({
		page,
	}) => {
		await page.goto("/waitlist?source=e2e&campaign=journey");
		await expect(
			page.getByText(
				/Registration is open|Registration is closed|Request access/,
				{ exact: true },
			),
		).toBeVisible();
	});

	test("invitation credential is removed from browser-visible history", async ({
		page,
		context,
	}) => {
		const credential = "a".repeat(43);
		const inspection = page.waitForResponse(
			(response) =>
				response.url().endsWith("/invitations/accept/api") &&
				response.request().method() === "POST",
		);
		await page.goto(`/invitations/accept?token=${credential}&campaign=journey`);

		expect((await inspection).status()).toBe(404);
		await expect(page).toHaveURL(/\/invitations\/accept\?campaign=journey$/);
		await expect(
			page.getByText("Organization invitation", { exact: true }),
		).toBeVisible();
		await expect(page.locator("body")).not.toContainText(credential);
		expect(
			(await context.cookies()).find(
				(cookie) => cookie.name === "invitation_return_token",
			),
		).toMatchObject({
			httpOnly: true,
			path: "/invitations/accept",
			sameSite: "Strict",
			value: credential,
		});
	});
});
