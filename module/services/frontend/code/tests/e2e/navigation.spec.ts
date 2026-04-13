import { test, expect } from "@playwright/test";

// Helper: log in as Sarah Chen (super_admin) before each test
async function loginAsSuperAdmin(page: import("@playwright/test").Page) {
  await page.goto("/auth/login");
  await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 10000 });
  await page.getByText("Sarah Chen").click();
  await page.waitForURL("/", { timeout: 15000 });
}

test.describe("Dashboard navigation", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
  });

  test("can navigate to Users page", async ({ page }) => {
    await page.getByRole("link", { name: "Users" }).click();
    await page.waitForURL(/\/users/);
    await expect(page.locator("h1, h2").first()).toBeVisible();
  });

  test("can navigate to Organizations page", async ({ page }) => {
    await page.getByRole("link", { name: "Organizations" }).click();
    await page.waitForURL(/\/organizations/);
  });

  test("can navigate to Teams page", async ({ page }) => {
    await page.getByRole("link", { name: "Teams" }).click();
    await page.waitForURL(/\/teams/);
  });

  test("can navigate to Audit Log page", async ({ page }) => {
    await page.getByRole("link", { name: "Audit Log" }).click();
    await page.waitForURL(/\/audit-log/);
  });

  test("can navigate to API Keys page", async ({ page }) => {
    await page.getByRole("link", { name: "API Keys" }).click();
    await page.waitForURL(/\/api-keys/);
  });

  test("can navigate to Settings > Security page", async ({ page }) => {
    await page.getByRole("link", { name: "Security (MFA)" }).click();
    await page.waitForURL(/\/settings\/mfa/);
  });

  test("can navigate to Pricing page", async ({ page }) => {
    await page.getByRole("link", { name: "Pricing" }).click();
    await page.waitForURL(/\/pricing/);
  });

  test("logout returns to login page", async ({ page }) => {
    // Open user menu and click sign out
    await page.getByText("Sign out").click();
    await page.waitForURL(/\/auth\/login/, { timeout: 10000 });
  });
});
