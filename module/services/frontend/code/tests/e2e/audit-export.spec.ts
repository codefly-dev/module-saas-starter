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

  test("Save with unreachable endpoint surfaces the pre-flight error", async ({
    page,
  }) => {
    // The api runs a connection probe (BucketExists) before persisting
    // any row. A typo in the endpoint or wrong creds returns
    // InvalidArgument, which the FE renders as a toast — the operator
    // doesn't get a green "Saved" only to discover failure 60 min
    // later in last_error.
    //
    // We point at port 1 which is guaranteed to refuse TCP — the
    // pre-flight returns "connection refused" and the row never
    // hits postgres.
    await pickAcmeOrg(page);

    await expect(page.getByLabel(/^bucket$/i)).toHaveValue("");
    const saveBtn = page.getByRole("button", { name: /^save$/i });
    await expect(saveBtn).toBeVisible();

    await page.getByLabel(/^bucket$/i).fill("does-not-matter");
    await page
      .getByLabel(/^endpoint/i)
      .fill("http://127.0.0.1:1");
    await page.getByLabel(/^access key id$/i).fill("anykey");
    await page.getByLabel(/^secret access key/i).fill("anysecret");

    await saveBtn.click();

    // sonner renders the error message in the description slot. We
    // accept any of the sub-strings the upstream connect-refused
    // path emits (varies a bit by platform/curl/minio version).
    await expect(page.getByText(/save failed|connection probe/i)).toBeVisible({
      timeout: 10_000,
    });

    // Confirm we're still in first-config mode — the row never
    // persisted. CTA still reads "Save", not "Update".
    await expect(
      page.getByRole("button", { name: /^save$/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: /^update$/i }),
    ).toHaveCount(0);
  });
});
