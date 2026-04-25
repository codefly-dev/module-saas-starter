// SSO admin E2E. The api runs in stub mode (WORKOS_API_KEY unset in
// the test fixtures) so StartSetup returns a placeholder URL pointing
// at /admin/sso?demo=1. We can verify the full state-machine —
// unconfigured → linked → disabled — without real WorkOS credentials.

import { test, expect, type Page } from "@playwright/test";

async function loginAsSuperAdmin(page: Page) {
  await page.goto("/auth/login");
  await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
  await page.getByText("Sarah Chen").click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
}

async function pickAcmeOrg(page: Page) {
  const trigger = page.getByRole("combobox").first();
  await trigger.click();
  await page
    .getByRole("option", { name: /acme corp/i })
    .click({ timeout: 15_000 });
}

test.describe("SSO admin page", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
    await page.goto("/admin/sso");
    await expect(
      page.getByRole("heading", { name: /single sign-on/i }),
    ).toBeVisible();
  });

  test("no-org empty state", async ({ page }) => {
    await expect(
      page.getByText(/select an organization/i),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^set up sso$/i }),
    ).toHaveCount(0);
  });

  test("unconfigured org shows the Set up SSO CTA", async ({ page }) => {
    await pickAcmeOrg(page);
    // Default state: no row exists for the org → "Not configured"
    // badge + "Set up SSO" CTA visible.
    await expect(page.getByText(/not configured/i)).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^set up sso$/i }),
    ).toBeVisible();
  });

  test("StartSetup transitions the org to linked + Disable clears it", async ({
    page,
  }) => {
    await pickAcmeOrg(page);

    // Stub mode (WORKOS_API_KEY unset): StartSetup persists status=
    // "linked" and returns demo URL. The page redirects there; we
    // intercept the navigation and stop on the demo URL so the
    // browser doesn't actually leave (which would un-mount the
    // tests' page object). Then we go back to /admin/sso to assert
    // the linked state.
    //
    // Easier path: capture the navigation but DON'T let it complete —
    // we just want to verify the row was written. We do that by
    // returning to /admin/sso after the click and re-querying.
    const startBtn = page.getByRole("button", { name: /^set up sso$/i });
    // Detach navigation by listening once and re-routing back.
    page.once("framenavigated", async () => {
      // Wait briefly then go back to the admin page so the test
      // assertions run on a stable page.
    });
    await Promise.all([
      page.waitForURL(/.*/),
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
    await page
      .getByRole("button", { name: /disable sso/i })
      .click();
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
