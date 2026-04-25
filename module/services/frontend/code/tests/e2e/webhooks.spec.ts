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

    // Heading visible. exact: true — the EmptyState title also
    // contains "Webhooks" ("Select an organization to view webhooks"),
    // so a non-exact match would trigger a strict-mode violation.
    await expect(
      page.getByRole("heading", { name: "Webhooks", exact: true }),
    ).toBeVisible();

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

    // OrgSelector is a Radix Select (shadcn primitive) — opens an
    // in-page popover, options arrive once useOrganizations resolves.
    const trigger = page.getByRole("combobox").first();
    await trigger.click();
    await page
      .getByRole("option", { name: /acme corp/i })
      .click({ timeout: 15_000 });

    // Empty state goes away, Create button appears.
    await expect(
      page.getByText(/select an organization to view webhooks/i),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: /create webhook/i }),
    ).toBeVisible();
  });
});

// ─────────────────────────────────────────────────────────────────
// v2 paths: replay + rotate. These run end-to-end through the api
// (sync WebhookSender on Test/Replay, RotateWebhookSecret service
// method that mints a new secret and shows it once). The endpoint
// URL is a public httpbin which always 404s on POSTs to /status/404
// — fine for replay testing because we only care that *some*
// delivery row exists with httpStatus filled in.
// ─────────────────────────────────────────────────────────────────

async function pickAcmeOrg(page: Page) {
  await page.goto("/admin/webhooks");
  const trigger = page.getByRole("combobox").first();
  await trigger.click();
  await page
    .getByRole("option", { name: /acme corp/i })
    .click({ timeout: 15_000 });
  await expect(
    page.getByRole("button", { name: /create webhook/i }),
  ).toBeVisible();
}

// Each v2 test creates its own webhook with a unique URL so replay
// counts and rotate dialogs don't collide across tests.
async function createWebhook(page: Page, url: string) {
  await page.getByRole("button", { name: /create webhook/i }).click();
  await page.getByLabel(/endpoint url/i).fill(url);
  // Tick the first event checkbox — "User Created" maps to
  // user.created which the FE understands. The form gates submit on
  // events.length > 0 so we must check at least one.
  await page.getByRole("checkbox", { name: /user created/i }).check();
  await page
    .getByRole("button", { name: /^create webhook$/i })
    .last()
    .click();
  // The toast is the cleanest "create RPC succeeded" signal — without
  // it, tanstack-query may not have invalidated the list yet.
  await expect(page.getByText(/^webhook created$/i)).toBeVisible({
    timeout: 10_000,
  });
}

// Locator for the actions-cell trigger on the webhook row whose URL
// contains `urlSubstring`. The trigger has no accessible name (it's
// just an icon-only Button), so we scope by row text + take the last
// button in the row, which is the column-defined dropdown trigger.
function actionsTriggerForUrl(page: Page, urlSubstring: string) {
  return page.getByRole("row", { name: new RegExp(urlSubstring, "i") })
    .getByRole("button")
    .last();
}

test.describe("Webhooks v2 — replay + rotate", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsSuperAdmin(page);
    await pickAcmeOrg(page);
  });

  test("Replay re-fires the delivery and a new attempt row appears", async ({
    page,
  }) => {
    const url = `https://example.com/webhook-replay-${Date.now()}`;
    await createWebhook(page, url);

    // Trigger a Test delivery so the deliveries panel has at least
    // one row to replay. WebhookSender.Send runs synchronously, so
    // by the time the success toast lands the row is in the db.
    await actionsTriggerForUrl(page, "webhook-replay").click();
    await page.getByRole("menuitem", { name: /^test$/i }).click();
    await expect(page.getByText(/test delivery sent/i)).toBeVisible({
      timeout: 10_000,
    });

    // Click the row itself to open the deliveries inspector.
    await page
      .getByRole("row", { name: /webhook-replay/i })
      .first()
      .click();

    const panel = page.locator('[role="dialog"], section').filter({
      hasText: /deliveries for/i,
    });
    // Fallback: the panel renders as a Card, not a dialog. Just wait
    // on the heading text.
    await expect(page.getByText(/deliveries for/i)).toBeVisible({
      timeout: 10_000,
    });

    // At this point the left rail should show one delivery row. We
    // count by the "User Created" event label which appears in each
    // row's truncated event-type line.
    const beforeCount = await page
      .getByText("User Created", { exact: true })
      .count();
    expect(beforeCount).toBeGreaterThanOrEqual(1);

    // The detail pane on the right has the Replay button — there's
    // exactly one in the panel.
    await page.getByRole("button", { name: /^replay$/i }).click();
    await expect(page.getByText(/replayed delivery/i)).toBeVisible({
      timeout: 10_000,
    });

    // The list invalidates + refetches. Wait for an additional row to
    // appear. We can't be certain the count strictly increased (the
    // refetch could race), so poll until we see >= beforeCount + 1 or
    // give up after 10s.
    await expect(async () => {
      const now = await page.getByText("User Created", { exact: true }).count();
      expect(now).toBeGreaterThanOrEqual(beforeCount + 1);
    }).toPass({ timeout: 10_000 });

    // Suppress unused lint on the panel locator — we kept it as a
    // documented attempt to scope strictly, falling back to text.
    void panel;
  });

  test("Rotate secret surfaces the one-shot New-Secret dialog", async ({
    page,
  }) => {
    const url = `https://example.com/webhook-rotate-${Date.now()}`;
    await createWebhook(page, url);

    // RotateSecret asks the operator to confirm via window.confirm —
    // playwright surfaces native dialogs through page.on("dialog").
    page.once("dialog", (d) => d.accept());

    await actionsTriggerForUrl(page, "webhook-rotate").click();
    await page.getByRole("menuitem", { name: /rotate secret/i }).click();

    // The RotatedSecretDialog renders as an actual <Dialog> with the
    // "New signing secret" title. The "I've saved it" CTA stays
    // disabled until Copy succeeds — proves the friction is wired.
    const dialog = page.getByRole("dialog");
    await expect(
      dialog.getByRole("heading", { name: /new signing secret/i }),
    ).toBeVisible({ timeout: 10_000 });
    await expect(
      dialog.getByRole("button", { name: /i've saved it/i }),
    ).toBeDisabled();

    // The Copy button click triggers navigator.clipboard.writeText.
    // Playwright's chromium grants clipboard permission to localhost
    // by default; if the click rejects we still want the test to fail
    // here loudly rather than further down.
    await dialog.getByRole("button", { name: /copy secret/i }).click();
    await expect(
      dialog.getByRole("button", { name: /i've saved it/i }),
    ).toBeEnabled({ timeout: 5_000 });

    await dialog.getByRole("button", { name: /i've saved it/i }).click();
    await expect(dialog).toHaveCount(0);
  });
});
