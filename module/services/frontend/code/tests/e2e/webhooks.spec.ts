// Webhooks page E2E. Covers the org-selector wiring fix
// (webhooks-page.tsx previously used a hardcoded "default" org id; now
// uses the same OrgSelector component as teams / api-keys / invitations
// / entitlements). Without an org selected, the page must show the
// "Select an organization" empty state and hide the Create button.
//
// Runs end-to-end through the Codefly fixture stack (frontend → accounts →
// postgres/vault/redis) — globalSetup spins up withDependencies before
// any test hits a network. Test pattern matches navigation.spec.ts:
// log in as super_admin, navigate, assert.

import { expect, type Page, test } from "@playwright/test";
import { resolveConsentPrompt } from "./consent";

async function loginAsSuperAdmin(page: Page) {
	await page.goto("/auth/login");
	await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 15000 });
	await page.getByText("Sarah Chen").click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await resolveConsentPrompt(page);
	await expect
		.poll(async () =>
			(await page.context().cookies()).some(
				(item) => item.name === "codefly_rt" && item.value.length > 0,
			),
		)
		.toBe(true);
}

test.describe("Webhooks admin page", () => {
	test.beforeEach(async ({ page }) => {
		await loginAsSuperAdmin(page);
	});

	test("shows the authenticated organization's webhooks by default", async ({
		page,
	}) => {
		await page.getByRole("button", { name: "Admin", exact: true }).click();
		await page.getByRole("link", { name: "Webhooks" }).click();
		await page.waitForURL(/\/admin\/webhooks/);

		// Heading visible. exact: true — the EmptyState title also
		// contains "Webhooks" ("Select an organization to view webhooks"),
		// so a non-exact match would trigger a strict-mode violation.
		await expect(
			page.getByRole("heading", { name: "Webhooks", exact: true }),
		).toBeVisible();

		await expect(
			page.getByText(/select an organization to view webhooks/i),
		).toHaveCount(0);
		await expect(
			page.getByRole("button", { name: /create webhook/i }),
		).toBeVisible();
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

	// Create returns the signing secret once, using the same guarded dialog as
	// rotation. Acknowledge it before interacting with the table behind the
	// modal; the page is intentionally inert until the operator copies it.
	const secretDialog = page.getByRole("dialog", {
		name: /new signing secret/i,
	});
	await expect(secretDialog).toBeVisible();
	await secretDialog.getByRole("button", { name: /copy secret/i }).click();
	await secretDialog.getByRole("button", { name: /i've saved it/i }).click();
	await expect(secretDialog).toHaveCount(0);

	await page.reload();
	await expect(actionsTriggerForUrl(page, url)).toBeVisible({
		timeout: 10_000,
	});
}

// Use the trigger's exact accessible name so retries remain deterministic even
// when a developer keeps fixture data from an earlier local run.
function actionsTriggerForUrl(page: Page, url: string) {
	return page.getByRole("button", { name: `Actions for ${url}`, exact: true });
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
		await actionsTriggerForUrl(page, url).click();
		await page.getByRole("menuitem", { name: /^test$/i }).click();
		await expect(page.getByText(/test delivery queued/i)).toBeVisible({
			timeout: 10_000,
		});

		// Click the row itself to open the deliveries inspector.
		await page
			.getByRole("row", {
				name: new RegExp(url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "i"),
			})
			.click();

		const panel = page.getByRole("region", {
			name: `Deliveries for ${url}`,
			exact: true,
		});
		await expect(panel).toBeVisible({
			timeout: 10_000,
		});

		const deliveryRows = panel.getByRole("button", { name: /^delivery /i });
		await expect(deliveryRows.first()).toBeVisible({ timeout: 10_000 });
		const beforeCount = await deliveryRows.count();
		expect(beforeCount).toBeGreaterThanOrEqual(1);

		// The detail pane on the right has the Replay button — there's
		// exactly one in the panel.
		await page.getByRole("button", { name: /^replay$/i }).click();
		await expect(page.getByText(/replay queued/i)).toBeVisible({
			timeout: 10_000,
		});

		// The replay mutation invalidates and refetches this subscription's list.
		await expect(deliveryRows).toHaveCount(beforeCount + 1, {
			timeout: 10_000,
		});
	});

	test("Rotate secret requires fresh MFA assurance", async ({
		page,
	}) => {
		const url = `https://example.com/webhook-rotate-${Date.now()}`;
		await createWebhook(page, url);

		// RotateSecret asks the operator to confirm via window.confirm —
		// playwright surfaces native dialogs through page.on("dialog").
		page.once("dialog", (d) => d.accept());

		await actionsTriggerForUrl(page, url).click();
		await page.getByRole("menuitem", { name: /rotate secret/i }).click();

		await expect(
			page.getByText("Couldn't rotate secret", { exact: true }),
		).toBeVisible({ timeout: 10_000 });
		await expect(
			page.getByText(/mfa_required/),
		).toBeVisible();
	});
});
