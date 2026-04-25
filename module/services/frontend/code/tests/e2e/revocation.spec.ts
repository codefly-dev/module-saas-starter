// Access-token revocation regression. The OWASP refresh-rotation
// pattern (already in place) only invalidates the refresh chain on
// logout — without an access-token revocation list, the *current*
// access token stays valid until natural expiry (15 min default).
// We added a Redis-backed JTI list (cache.NewTokenRevoker wired in
// work.go) that Logout writes into; VerifyAccess consults it on
// every authenticated call.
//
// This spec proves the wiring end-to-end: log in, capture access
// token, log out, then re-use the captured token → server rejects
// it.

import { test, expect, type Page } from "@playwright/test";

const API_REST = process.env.API_REST ?? "http://localhost:5962";
const API_CONNECT = process.env.API_CONNECT ?? "http://localhost:44790";

async function loginAndGrabTokens(
  page: Page,
  fixtureName: string,
): Promise<{ accessToken: string; refreshToken: string }> {
  await page.goto("/auth/login");
  await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
  await page.getByText(fixtureName).click();
  await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });

  const refreshToken = await page.evaluate(() =>
    localStorage.getItem("codefly_refresh_token"),
  );
  if (!refreshToken) throw new Error("no refresh token after login");

  // Trade the (already-rotated) refresh for a fresh access token so
  // we have BOTH halves of a current pair to play with.
  const res = await fetch(`${API_REST}/v1/auth/refresh`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  if (!res.ok) {
    throw new Error(`refresh failed: ${res.status} ${await res.text()}`);
  }
  const data = (await res.json()) as { accessToken: string; refreshToken: string };
  return data;
}

async function authedRPC(
  service: string,
  method: string,
  body: Record<string, unknown>,
  bearer: string,
): Promise<{ status: number; body: string }> {
  const res = await fetch(`${API_CONNECT}/${service}/${method}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${bearer}`,
    },
    body: JSON.stringify(body),
  });
  return { status: res.status, body: await res.text() };
}

test.describe("Access-token revocation", () => {
  test("logout invalidates the access token immediately", async ({ page }) => {
    const { accessToken, refreshToken } = await loginAndGrabTokens(
      page,
      "Sarah Chen",
    );

    // Sanity: the access token works BEFORE logout — proves the
    // bearer is good and the test isn't measuring an unrelated
    // failure when we try it again after logout.
    const before = await authedRPC(
      "customers.UserService",
      "GetSelf",
      {},
      accessToken,
    );
    expect(before.status).toBe(200);

    // Log out — this should add the access JTI to the revocation list
    // AND revoke the refresh family.
    const logoutRes = await fetch(`${API_REST}/v1/auth/logout`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${accessToken}`,
      },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
    expect(logoutRes.ok).toBe(true);

    // Same access token, same call, AFTER logout. With the revocation
    // list wired (cache.NewTokenRevoker → Redis), this should be
    // rejected. Without it, the access token would still work for the
    // remainder of its 15-min lifetime.
    const after = await authedRPC(
      "customers.UserService",
      "GetSelf",
      {},
      accessToken,
    );
    // Either the bearer fails to validate (interceptor's IsRevoked
    // check) OR the handler reports "not found" because the request
    // never made it past auth. We just need a non-2xx — anything in
    // [400, 599) confirms revocation fired.
    expect(after.status).toBeGreaterThanOrEqual(400);
    expect(after.status).toBeLessThan(600);
  });

  test("logout invalidates the refresh token (OWASP rotation)", async ({ page }) => {
    // Complement of the above: refresh half. Already covered by the
    // existing minter unit tests, but we lock it in here against the
    // full stack so a future refactor of the session store can't
    // silently regress refresh revocation while passing access-token
    // revocation.
    const { refreshToken } = await loginAndGrabTokens(page, "Sarah Chen");

    const logoutRes = await fetch(`${API_REST}/v1/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
    expect(logoutRes.ok).toBe(true);

    // Try to use the revoked refresh — should fail.
    const refreshRes = await fetch(`${API_REST}/v1/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
    expect(refreshRes.ok).toBe(false);
  });
});
