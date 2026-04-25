// Audit-export admin page E2E. End-to-end: FE → gateway → api →
// postgres. The api persists the AuditExportConfig row, the FE Get
// returns it (with secretAccessKey masked to ""), and the form
// re-renders into edit mode. The exporter goroutine runs in the
// background but won't tick within the test window — that's covered
// by backend integration tests.
//
// Pattern matches webhooks.spec.ts: log in as super_admin via
// fixture, pick Acme Corp, exercise the form.

import { test, expect, type Page } from "@playwright/test";

async function loginAsSuperAdmin(page: Page) {
  await page.goto("/auth/login");
  await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
  await page.getByText("Sarah Chen").click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
}

async function pickAcmeOrg(page: Page) {
  // OrgSelector is a Radix Select trigger; same pattern as the
  // webhooks page. Options arrive once useOrganizations resolves.
  const trigger = page.getByRole("combobox").first();
  await trigger.click();
  await page
    .getByRole("option", { name: /acme corp/i })
    .click({ timeout: 15_000 });
}

test.describe("Audit Export admin page", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
    await page.goto("/admin/audit-export");
    // Heading must render before the OrgSelector — exact match
    // because the EmptyState below also says "audit-export".
    await expect(
      page.getByRole("heading", { name: "Audit Export", exact: true }),
    ).toBeVisible();
  });

  test("no-org empty state shows the org-picker prompt", async ({ page }) => {
    // EmptyState title is "Select an organization" — appears only
    // when no org is picked. Save button is gated until an org is
    // selected because the form lives behind the org check.
    await expect(
      page.getByText(/select an organization/i),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^save$/i }),
    ).toHaveCount(0);
  });

  test("first-config flow saves the row and flips to edit mode", async ({
    page,
  }) => {
    await pickAcmeOrg(page);

    // First-config: form is blank, CTA reads "Save". Pre-bucket
    // assertion proves we landed on the new-config branch and not
    // the existing-config (edit) branch.
    await expect(page.getByLabel(/^bucket$/i)).toHaveValue("");
    const saveBtn = page.getByRole("button", { name: /^save$/i });
    await expect(saveBtn).toBeVisible();

    // Use a unique bucket name per run so the postgres row is
    // distinct across reruns of the test against the shared fixture.
    const bucket = `audit-test-${Date.now()}`;

    await page.getByLabel(/^bucket$/i).fill(bucket);
    await page.getByLabel(/^region$/i).fill("us-east-1");
    await page
      .getByLabel(/^endpoint/i)
      .fill("http://localhost:9000");
    await page.getByLabel(/^access key id$/i).fill("minioadmin");
    await page.getByLabel(/^secret access key/i).fill("minioadmin");

    await saveBtn.click();
    await expect(page.getByText(/audit export saved/i)).toBeVisible({
      timeout: 10_000,
    });

    // Edit mode: bucket pre-filled, CTA flips to "Update", secret
    // input has the masked placeholder. The "(preserved)" hint
    // proves the api Get returned "" for secretAccessKey, which is
    // the contract we depend on.
    await expect(page.getByLabel(/^bucket$/i)).toHaveValue(bucket);
    await expect(
      page.getByRole("button", { name: /^update$/i }),
    ).toBeVisible({ timeout: 10_000 });
    await expect(
      page.getByText(/leave blank to keep existing/i),
    ).toBeVisible();
  });

  test("delete flow removes the configuration", async ({ page }) => {
    await pickAcmeOrg(page);

    // Either the previous test seeded a row or we need to make one.
    // Either way, ensure a row exists for the delete to act on.
    const updateCTA = page.getByRole("button", { name: /^update$/i });
    if (!(await updateCTA.isVisible().catch(() => false))) {
      const bucket = `audit-test-delete-${Date.now()}`;
      await page.getByLabel(/^bucket$/i).fill(bucket);
      await page.getByLabel(/^access key id$/i).fill("minioadmin");
      await page.getByLabel(/^secret access key/i).fill("minioadmin");
      await page.getByRole("button", { name: /^save$/i }).click();
      await expect(page.getByText(/audit export saved/i)).toBeVisible({
        timeout: 10_000,
      });
    }

    // The Remove button uses window.confirm — accept the dialog
    // before clicking so the mutation actually fires.
    page.once("dialog", (d) => d.accept());

    await page
      .getByRole("button", { name: /remove configuration/i })
      .click();
    await expect(page.getByText(/audit export disabled/i)).toBeVisible({
      timeout: 10_000,
    });

    // Back to first-config state: Save CTA returns, Update vanishes.
    await expect(
      page.getByRole("button", { name: /^save$/i }),
    ).toBeVisible({ timeout: 10_000 });
    await expect(
      page.getByRole("button", { name: /^update$/i }),
    ).toHaveCount(0);
  });
});
