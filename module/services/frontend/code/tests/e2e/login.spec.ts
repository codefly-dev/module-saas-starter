import { signInForScenario } from "./fixture-session";
import { expect, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

test.describe("Login flow", () => {
	test("login page loads without sidebar", async ({ page }) => {
		await page.goto("/auth/login");

		// Should show the login card
		await expect(page.getByText("Sign in")).toBeVisible();

		// Should NOT show the sidebar/admin nav
		await expect(page.locator("text=Dashboard")).not.toBeVisible();
		await expect(page.locator("text=Users & Access")).not.toBeVisible();
	});

	test("fixture users are displayed in dev mode", async ({ page }) => {
		await page.goto("/auth/login");

		const choices = page.getByRole("group", { name: "Development sign-in — choose a person" });
		await expect(choices.getByRole("button")).toHaveCount(3);
		for (const name of ["Dana Lee", "Carol Davis", "Bob Williams"]) {
			await expect(choices.getByRole("button", { name: new RegExp(name) })).toBeVisible();
		}
		await expect(page.getByText("Sarah Chen")).toHaveCount(0);
		await expect(page.getByText("Alice Johnson")).toHaveCount(0);
	});

	test("clicking fixture user logs in and redirects to dashboard", async ({
		page,
	}) => {
		await page.goto("/auth/login");

		await page.getByRole("button", { name: /Dana Lee/ }).click();

		// Should redirect to dashboard
		await page.waitForURL("/", { timeout: 15000 });

		// Should see the welcome page
		await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible();
	});

	test("super_admin sees admin navigation", async ({ page }) => {
		await signInForScenario(page, "Sarah Chen");
		await page.waitForURL("/", { timeout: 15000 });
		await resolveConsentPrompt(page);
		await page.getByRole("button", { name: "Admin", exact: true }).click();

		// Admin nav sections should be visible for super_admin. Use exact
		// matching to pin to the sidebar group headers — without `exact`,
		// "Platform" also matches the "Platform Users" link and trips
		// Playwright's strict-mode violation.
		await expect(
			page.getByText("Users & Access", { exact: true }),
		).toBeVisible();
		await expect(
			page.getByRole("link", { name: "Platform Users", exact: true }),
		).toBeVisible();
	});

	test("unauthenticated user redirected to login", async ({ page }) => {
		// Try to access an admin page directly. The (admin) route group's
		// layout redirects unauthenticated callers to /auth/login; non-admin
		// signed-in users get sent back to /.
		await page.goto("/admin/users");

		// Should redirect to login
		await page.waitForURL(/\/auth\/login/, { timeout: 10000 });
		await expect(page.getByText("Sign in")).toBeVisible();
	});

	test("a member sees the Admin refusal and can return to the dashboard", async ({ page }) => {
		await page.goto("/auth/login");
		await expect(page.getByText("Bob Williams")).toBeVisible({
			timeout: 10000,
		});
		await page.getByText("Bob Williams").click();
		await page.waitForURL("/", { timeout: 15000 });

		await resolveConsentPrompt(page);
		await page.goto("/admin/users");
		await expect(page.getByRole("heading", { name: "You don't have access to Admin" })).toBeVisible();
		await expect(page.getByRole("heading", { name: "Platform Users" })).toHaveCount(0);
		await page.getByRole("link", { name: "Back to the dashboard" }).click();
		await page.waitForURL("/", { timeout: 10000 });
		await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible();
	});
});
