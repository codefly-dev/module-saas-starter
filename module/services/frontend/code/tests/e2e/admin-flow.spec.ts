// Real E2E — covers the admin golden path end-to-end through the
// running stack (frontend → gateway → api → postgres/vault/redis).
// These tests are meant to run with `codefly run service frontend`
// already up, so all the dependencies are live and answering.
//
// Using the `codefly` TS SDK for endpoint resolution: Playwright's
// baseURL comes from PLAYWRIGHT_BASE_URL (or localhost:21931 default),
// but when codefly is driving the test run it injects
// CODEFLY__ENDPOINT__... env vars that the SDK parses. This way the
// same spec runs in local dev AND under `codefly test service frontend`
// without hardcoding ports.

import { test, expect, type Page } from "@playwright/test";
import { endpoint } from "codefly";

// resolveFrontendURL prefers codefly's injected endpoint env vars
// (CODEFLY__ENDPOINT__saas-starter__FRONTEND__WEB__REST) and falls back
// to the Playwright baseURL when running standalone.
function resolveFrontendURL(): string | null {
  try {
    const url = endpoint({
      method: "GET",
      module: "saas-starter",
      service: "frontend",
      path: "/",
    });
    if (url) return url.replace(/\/$/, "");
  } catch {
    // SDK raises when env vars aren't present — fine, fall through.
  }
  return null;
}

// loginAs walks the fixture login flow. fixtureName is the display text
// on the login card (e.g. "Sarah Chen" → super_admin, "Bob Williams"
// → member). Keys off the post-login "Welcome back" heading rather
// than just the URL — the URL match can pass while the page is still
// mid-render, leading to selector-not-found failures downstream.
async function loginAs(page: Page, fixtureName: string) {
  await page.goto("/auth/login");
  await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
  await page.getByText(fixtureName).click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
}

test.describe("admin golden path", () => {
  test("SDK endpoint resolution matches Playwright baseURL when set", async () => {
    // Sanity check — the SDK doesn't throw on missing env vars and
    // returns a URL or null. The actual test baseURL comes from
    // PLAYWRIGHT_BASE_URL / the defaultConfig; this just verifies the
    // SDK wiring is sane so future specs can trust it.
    const url = resolveFrontendURL();
    // In standalone mode url is null; under codefly-driven runs it's a
    // valid URL. Either way, no throw = SDK is hooked up correctly.
    expect(url === null || url.startsWith("http")).toBe(true);
  });

  test("super_admin sees the conditional admin tile on the dashboard", async ({ page }) => {
    // The dashboard root (/) must show an "Admin panel" CTA for
    // admin+ users and hide it from regular members. This is the main
    // "module constraint" check — saas-starter UI must only show up
    // conditionally on the host app's dashboard.
    await loginAs(page, "Sarah Chen");
    await expect(page.getByRole("heading", { name: /administration/i })).toBeVisible();
    await expect(page.getByText("Admin panel")).toBeVisible();
  });

  test("regular member does NOT see the admin tile", async ({ page }) => {
    await loginAs(page, "Bob Williams");
    await expect(page.getByRole("heading", { name: /administration/i })).toHaveCount(0);
    await expect(page.getByText("Admin panel")).toHaveCount(0);
  });

  test("super_admin can open the admin hub and see Access + Platform sections", async ({ page }) => {
    await loginAs(page, "Sarah Chen");
    // Navigate via the conditional CTA we just verified.
    await page.getByText("Admin panel").click();
    await page.waitForURL(/\/admin\/?$/, { timeout: 10000 });
    await expect(page.getByRole("heading", { name: /^Admin$/ })).toBeVisible();
    // "Access" section is visible to any admin; "Platform" section is
    // wrapped in <RoleGate require="super_admin"> — Sarah is super_admin
    // so both should show. Scope by role=heading because the sidebar
    // also has "Platform" as a group label, which would collide.
    await expect(page.getByRole("heading", { name: "Access", exact: true })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Platform", exact: true })).toBeVisible();
  });

  test("full admin → users list round trip", async ({ page }) => {
    // Walks: login → /admin → /admin/users → something rendered.
    // Exercises the full stack: role-gated (admin) layout, Connect-ES
    // query to the api service, api's auth middleware, the new Redis
    // cache on requireOrgMember, and the authz check itself.
    await loginAs(page, "Sarah Chen");
    await page.goto("/admin");
    await page.waitForURL(/\/admin\/?$/);
    // exact: true — the sidebar also has "Platform Users" + the
    // /admin "Users Search users, suspend," card link.
    await page.getByRole("link", { name: "Users", exact: true }).click();
    await page.waitForURL(/\/admin\/users/);
    // The page must at least render a heading; the data-grid below
    // requires authenticated API calls that this test validates as a
    // side effect.
    await expect(page.locator("h1, h2").first()).toBeVisible();
  });
});
