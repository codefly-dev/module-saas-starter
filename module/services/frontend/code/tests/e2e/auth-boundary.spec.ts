// Auth-boundary regression. Closes the gap that the navigation specs
// can't see: a non-admin who skips the FE entirely and hits the
// Connect transport directly. Without server-side enforcement (the
// requireOrgAdmin / requirePlatformAdmin gates), the UI alone is a
// suggestion. These tests prove the gates fire — same RPC, same org,
// different actor → permission_denied for the member, ok for the
// super_admin.
//
// Strategy: drive the api's Connect endpoint directly via fetch.
// Connect-Web speaks JSON-over-HTTP for unary RPCs, so a plain POST
// with `Content-Type: application/json` is the canonical bypass an
// attacker would use too. We grab the bearer by logging in via the
// FE's normal flow (sets sessionStorage) and reading the token off
// the cookie/state, then probe the api directly.

import { test, expect, type Page } from "@playwright/test";

const API_CONNECT = process.env.API_CONNECT ?? "http://localhost:44790";
const API_REST = process.env.API_REST ?? "http://localhost:5962";

async function loginAs(page: Page, fixtureName: string): Promise<string> {
  await page.goto("/auth/login");
  await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
  await page.getByText(fixtureName).click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });

  // Extract the access token from the auth refresh that fired on
  // the dashboard mount. The simplest reliable path: re-mint via the
  // refresh token in localStorage. The FE keeps refresh in localStorage
  // and access in React state — for a test probe we just trade the
  // refresh for a fresh access token.
  const refreshToken = await page.evaluate(() =>
    localStorage.getItem("codefly_refresh_token"),
  );
  if (!refreshToken) throw new Error("no refresh token after login");

  const res = await fetch(`${API_REST}/v1/auth/refresh`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  if (!res.ok) {
    throw new Error(`refresh failed: ${res.status} ${await res.text()}`);
  }
  const data = (await res.json()) as { accessToken: string };
  return data.accessToken;
}

async function rpc(
  service: string,
  method: string,
  body: Record<string, unknown>,
  bearer?: string,
): Promise<{ status: number; body: string }> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (bearer) headers["Authorization"] = `Bearer ${bearer}`;
  const res = await fetch(`${API_CONNECT}/${service}/${method}`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
  });
  return { status: res.status, body: await res.text() };
}

test.describe("Server-side auth boundary", () => {
  test("ListUsers requires authentication (no bearer → 401)", async () => {
    // ListUsers is platform-admin-only and NOT in the public allowlist.
    // A bare unauthenticated call should be rejected before the
    // handler even runs. Connect-Web maps gRPC Unauthenticated to HTTP
    // 401 — though some Connect implementations route to 16 (Unauth)
    // with different status mapping; accept either.
    const r = await rpc("customers.UserService", "ListUsers", {});
    expect(r.status).toBeGreaterThanOrEqual(400);
    expect(r.status).toBeLessThan(500);
    expect(r.body.toLowerCase()).toMatch(/unauthenticated|unauthorized|user id not found/);
  });

  test("ListUsers rejects a member's bearer (Bob → permission_denied)", async ({ page }) => {
    const bobToken = await loginAs(page, "Bob Williams");
    const r = await rpc("customers.UserService", "ListUsers", {}, bobToken);
    // Bob authenticates fine but lacks platform_admin → server returns
    // PermissionDenied, mapped to HTTP 403 by Connect.
    expect([401, 403]).toContain(r.status);
    expect(r.body.toLowerCase()).toMatch(/denied|forbidden|permission|admin/);
  });

  test("ListUsers accepts a super_admin's bearer (Sarah → 200)", async ({ page }) => {
    const sarahToken = await loginAs(page, "Sarah Chen");
    const r = await rpc("customers.UserService", "ListUsers", { pageSize: 10 }, sarahToken);
    expect(r.status).toBe(200);
    // Body is JSON-encoded ListUsersResponse; just confirm the shape.
    const json = JSON.parse(r.body) as { users?: unknown[] };
    expect(Array.isArray(json.users)).toBe(true);
  });

  test("BeginOAuth is in the public allowlist (no bearer → 200)", async () => {
    // The whole point of BeginOAuth is to be the entry point of a
    // login flow — it MUST be reachable without auth. Confirms the
    // public-procedure allowlist correctly includes it.
    const r = await rpc(
      "customers.AuthService",
      "BeginOAuth",
      { provider: "workos", redirectUri: "http://localhost:21931/auth/callback" },
    );
    // 200 = signer wired (prod path); 412 = signer not wired in this
    // dev stack but the RPC itself is reachable. Both prove auth
    // didn't reject; only 401 would mean the allowlist is broken.
    expect([200, 412]).toContain(r.status);
    if (r.status === 200) {
      const json = JSON.parse(r.body) as { state?: string };
      expect(json.state).toMatch(/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/);
    }
  });

  test("Authenticate is in the public allowlist (the chicken-and-egg root)", async () => {
    // Without /authenticate being public, no one could ever obtain a
    // bearer in the first place. Probe with bogus credentials so it
    // fails AT THE BUSINESS LAYER (provider not found, identity not
    // resolved) rather than at the auth interceptor — that proves
    // the request reached the handler.
    const r = await rpc(
      "customers.AuthService",
      "Authenticate",
      { provider: "fake-provider", providerId: "nope", providerEmail: "nope@example.com" },
    );
    // Reaches handler → 4xx with a business error in the body.
    // 401 here would mean the allowlist is wrong.
    expect(r.status).not.toBe(401);
  });
});
