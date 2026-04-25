import { test, expect } from "@playwright/test";

// Helper: log in as Sarah Chen (super_admin) before each test. Keys off
// the dashboard's "Welcome back" heading rather than a bare URL match —
// the middleware briefly redirects during auth and URL-only waits can
// resolve before the page is usable.
async function loginAsSuperAdmin(page: import("@playwright/test").Page) {
  await page.goto("/auth/login");
  await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
  await page.getByText("Sarah Chen").click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
}

test.describe("Dashboard navigation", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
  });

  // URL paths migrated in 2026-04-23: all admin surfaces now live under
  // /admin/* so a non-admin visiting the URL directly gets redirected by
  // the (admin) layout. The sidebar nav was updated to match.

  // exact: true on every sidebar nav click — the dashboard's
  // "Admin panel" card has "Users, organizations, teams,
  // permissions, API keys, audit log" as its accessible name, so a
  // non-exact match against any of those words ALSO hits that card.
  // The sidebar also has "Platform Users" which collides with "Users".

  test("can navigate to Users page", async ({ page }) => {
    await page.getByRole("link", { name: "Users", exact: true }).click();
    await page.waitForURL(/\/admin\/users/);
    await expect(page.locator("h1, h2").first()).toBeVisible();
  });

  test("can navigate to Organizations page", async ({ page }) => {
    await page.getByRole("link", { name: "Organizations", exact: true }).click();
    await page.waitForURL(/\/admin\/organizations/);
  });

  test("can navigate to Teams page", async ({ page }) => {
    await page.getByRole("link", { name: "Teams", exact: true }).click();
    await page.waitForURL(/\/admin\/teams/);
  });

  test("can navigate to Audit Log page", async ({ page }) => {
    await page.getByRole("link", { name: "Audit Log", exact: true }).click();
    await page.waitForURL(/\/admin\/audit-log/);
  });

  test("can navigate to API Keys page", async ({ page }) => {
    await page.getByRole("link", { name: "API Keys", exact: true }).click();
    await page.waitForURL(/\/admin\/api-keys/);
  });

  test("can navigate to Settings > Security page", async ({ page }) => {
    // Security (and other personal-settings links) live in the user-
    // avatar dropdown in the sidebar footer — NOT in the main nav. Open
    // the menu first, then click the item.
    await openUserMenu(page);
    await page.getByRole("menuitem", { name: "Security" }).click();
    await page.waitForURL(/\/settings\/mfa/);
  });

  test("can navigate to Pricing page", async ({ page }) => {
    await page.getByRole("link", { name: "Pricing" }).click();
    await page.waitForURL(/\/pricing/);
  });

  test("logout returns to login page", async ({ page }) => {
    await openUserMenu(page);
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await page.waitForURL(/\/auth\/login/, { timeout: 10000 });
  });
});

// openUserMenu clicks the user-avatar trigger in the sidebar footer so
// its dropdown (Security, Notifications, Data & Privacy, Sign out)
// becomes interactable. The trigger text is the user's email — Sarah
// Chen's fixture email is "admin@acme.com".
async function openUserMenu(page: import("@playwright/test").Page) {
  await page.getByRole("button", { name: /admin@acme\.com/ }).click();
}
