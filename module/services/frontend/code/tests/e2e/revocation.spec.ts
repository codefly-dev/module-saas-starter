import { signInForScenario } from "./fixture-session";
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

import { expect, type Page, test } from "@playwright/test";
async function loginAndGrabTokens(
	page: Page,
	fixtureName: string,
): Promise<{ accessToken: string; refreshToken: string }> {
	await signInForScenario(page, fixtureName);
	await expect(page.getByRole("heading", { name: /^Welcome back/ })).toBeVisible({ timeout: 20000 });

	await expect
		.poll(async () =>
			(await page.context().cookies()).some(
				(item) => item.name === "codefly_rt" && item.value.length > 0,
			),
		)
		.toBe(true);
	const cookie = (await page.context().cookies()).find(
		(item) => item.name === "codefly_rt",
	);
	if (!cookie?.value) throw new Error("no httpOnly refresh cookie after login");

	const refresh = await page.evaluate(async () => {
		const response = await fetch("/v1/auth/refresh", {
			method: "POST",
			credentials: "include",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({}),
		});
		return {
			status: response.status,
			body: await response.text(),
		};
	});
	if (refresh.status < 200 || refresh.status >= 300) {
		throw new Error(`refresh failed: ${refresh.status} ${refresh.body}`);
	}
	const data = JSON.parse(refresh.body) as { accessToken: string };
	await expect
		.poll(async () =>
			(await page.context().cookies()).some(
				(item) => item.name === "codefly_rt" && item.value !== cookie.value,
			),
		)
		.toBe(true);
	const rotated = (await page.context().cookies()).find(
		(item) => item.name === "codefly_rt",
	);
	if (!rotated?.value)
		throw new Error("refresh did not rotate the httpOnly cookie");
	return { accessToken: data.accessToken, refreshToken: rotated.value };
}

async function authedRPC(
	origin: string,
	service: string,
	method: string,
	body: Record<string, unknown>,
	bearer: string,
): Promise<{ status: number; body: string }> {
	const res = await fetch(`${origin}/${service}/${method}`, {
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
			new URL(page.url()).origin,
			"saas.accounts.v1.UserService",
			"GetSelf",
			{},
			accessToken,
		);
		expect(before.status).toBe(200);

		// Log out — this should add the access JTI to the revocation list
		// AND revoke the refresh family.
		const logoutRes = await fetch(`${new URL(page.url()).origin}/v1/auth/logout`, {
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
			new URL(page.url()).origin,
			"saas.accounts.v1.UserService",
			"GetSelf",
			{},
			accessToken,
		);
		expect(after.status).toBe(401);
	});

	test("logout invalidates the refresh token (OWASP rotation)", async ({
		page,
	}) => {
		// Complement of the above: refresh half. Already covered by the
		// existing minter unit tests, but we lock it in here against the
		// full stack so a future refactor of the session store can't
		// silently regress refresh revocation while passing access-token
		// revocation.
		const { refreshToken } = await loginAndGrabTokens(page, "Sarah Chen");

		const logoutRes = await fetch(`${new URL(page.url()).origin}/v1/auth/logout`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ refresh_token: refreshToken }),
		});
		expect(logoutRes.ok).toBe(true);

		// Try to use the revoked refresh — should fail.
		const refreshRes = await fetch(`${new URL(page.url()).origin}/v1/auth/refresh`, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ refresh_token: refreshToken }),
		});
		expect(refreshRes.status).toBe(401);
	});
});
