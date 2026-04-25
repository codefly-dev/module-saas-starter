// Webhooks page E2E. Covers the org-selector wiring fix
// (webhooks-page.tsx previously used a hardcoded "default" org id; now
// uses the same OrgSelector component as teams / api-keys / invitations
// / entitlements). Without an org selected, the page must show the
// "Select an organization" empty state and hide the Create button.
//
// Runs end-to-end through the full stack (frontend → gateway → api →
// postgres/vault/redis) — globalSetup spins up withDependencies before
// any test hits a network. Test pattern matches navigation.spec.ts:
// log in as super_admin, navigate, assert.

import { test, expect, type Page } from "@playwright/test";

async function loginAsSuperAdmin(page: Page) {
  await page.goto("/auth/login");
  await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
  await page.getByText("Sarah Chen").click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
}

test.describe("Webhooks admin page", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
  });

  test("shows OrgSelector and the no-org empty state by default", async ({ page }) => {
    await page.getByRole("link", { name: "Webhooks" }).click();
    await page.waitForURL(/\/admin\/webhooks/);

    // Heading visible.
    await expect(page.getByRole("heading", { name: "Webhooks" })).toBeVisible();

    // OrgSelector is the same trigger pattern other admin pages use —
    // pre-selection it shows a placeholder, not a chosen org name.
    // Empty state asks the user to pick an org.
    await expect(
      page.getByText(/select an organization to view webhooks/i),
    ).toBeVisible();

    // Create button is gated until an org is picked. Was visible
    // unconditionally before the fix.
    await expect(
      page.getByRole("button", { name: /create webhook/i }),
    ).toHaveCount(0);
  });

  test("picking an org reveals the table + Create button", async ({ page }) => {
    await page.goto("/admin/webhooks");

    // Open the OrgSelector dropdown. It's a Radix Select trigger —
    // the role-based locator is portable across the design-system
    // updates.
    const trigger = page.getByRole("combobox").first();
    await trigger.click();

    // dev-admin fixture seeds Acme Corp as the only org, so its name
    // is the only option to pick.
    await page.getByRole("option", { name: /acme corp/i }).click();

    // Empty state goes away, Create button appears.
    await expect(
      page.getByText(/select an organization to view webhooks/i),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: /create webhook/i }),
    ).toBeVisible();
  });
});
