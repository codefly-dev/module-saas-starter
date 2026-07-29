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

import { expect, type Page, test } from "@playwright/test";
import { resolveServiceAddressSync } from "codefly";

function mustResolve(
	service: string,
	type: "http" | "rest" | "connect",
): string {
	const address = resolveServiceAddressSync(service, type);
	if (!address)
		throw new Error(`cannot resolve Codefly endpoint ${service}/${type}`);
	return address;
}

const API_CONNECT =
	process.env.API_CONNECT ?? mustResolve("accounts", "connect");
const FRONTEND =
	process.env.PLAYWRIGHT_BASE_URL ?? mustResolve("frontend", "http");

async function loginAs(page: Page, fixtureName: string): Promise<string> {
	await page.goto("/auth/login");
	await expect(page.getByText(fixtureName)).toBeVisible({ timeout: 15000 });
	await page.getByText(fixtureName).click();
	await expect(page.getByText("Welcome back")).toBeVisible({ timeout: 20000 });
	await expect
		.poll(async () =>
			(await page.context().cookies()).some(
				(item) => item.name === "codefly_rt" && item.value.length > 0,
			),
		)
		.toBe(true);

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
	return data.accessToken;
}

async function rpc(
	service: string,
	method: string,
	body: Record<string, unknown>,
	bearer?: string,
): Promise<{ status: number; body: string }> {
	const headers: Record<string, string> = {
		"Content-Type": "application/json",
	};
	if (bearer) headers["Authorization"] = `Bearer ${bearer}`;
	const res = await fetch(`${API_CONNECT}/${service}/${method}`, {
		method: "POST",
		headers,
		body: JSON.stringify(body),
	});
	return { status: res.status, body: await res.text() };
}

function accessClaims(token: string): { sub: string; org: string } {
	const [, payload] = token.split(".");
	if (!payload) throw new Error("access token has no payload");
	return JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as {
		sub: string;
		org: string;
	};
}

test.describe("Server-side auth boundary", () => {
	test("ListUsers requires authentication (no bearer → 401)", async () => {
		// ListUsers is platform-admin-only and NOT in the public allowlist.
		// A bare unauthenticated call should be rejected before the
		// handler even runs. Connect-Web maps gRPC Unauthenticated to HTTP
		// 401 — though some Connect implementations route to 16 (Unauth)
		// with different status mapping; accept either.
		const r = await rpc("saas.accounts.v1.UserService", "ListUsers", {});
		expect(r.status).toBeGreaterThanOrEqual(400);
		expect(r.status).toBeLessThan(500);
		expect(r.body.toLowerCase()).toMatch(
			/unauthenticated|unauthorized|user id not found/,
		);
	});

	test("ListUsers rejects a member's bearer (Bob → permission_denied)", async ({
		page,
	}) => {
		const bobToken = await loginAs(page, "Bob Williams");
		const r = await rpc(
			"saas.accounts.v1.UserService",
			"ListUsers",
			{},
			bobToken,
		);
		// Bob authenticates fine but lacks platform_admin → server returns
		// PermissionDenied, mapped to HTTP 403 by Connect.
		expect([401, 403]).toContain(r.status);
		expect(r.body.toLowerCase()).toMatch(/denied|forbidden|permission|admin/);
	});

	test("ListUsers accepts a super_admin's bearer (Sarah → 200)", async ({
		page,
	}) => {
		const sarahToken = await loginAs(page, "Sarah Chen");
		const r = await rpc(
			"saas.accounts.v1.UserService",
			"ListUsers",
			{ pageSize: 10 },
			sarahToken,
		);
		expect(r.status).toBe(200);
		// Body is JSON-encoded ListUsersResponse; just confirm the shape.
		const json = JSON.parse(r.body) as { users?: unknown[] };
		expect(Array.isArray(json.users)).toBe(true);
	});

	test("member cannot substitute tenant/resource IDs into admin mutations", async ({
		page,
	}) => {
		const bobToken = await loginAs(page, "Bob Williams");
		const claims = accessClaims(bobToken);

		const teams = await rpc(
			"saas.accounts.v1.TeamService",
			"ListTeams",
			{ orgId: claims.org },
			bobToken,
		);
		expect(teams.status).toBe(200);
		const teamID = (JSON.parse(teams.body) as { teams?: Array<{ id: string }> })
			.teams?.[0]?.id;
		expect(teamID).toBeTruthy();

		const probes = [
			await rpc(
				"saas.accounts.v1.OrganizationService",
				"AddMember",
				{
					orgId: claims.org,
					userId: "00000000-0000-4000-8000-000000000001",
					role: "ORG_ROLE_MEMBER",
				},
				bobToken,
			),
			await rpc(
				"saas.accounts.v1.TeamService",
				"RemoveMember",
				{
					teamId: teamID,
					userId: claims.sub,
				},
				bobToken,
			),
			await rpc(
				"saas.accounts.v1.APIKeyService",
				"CreateAPIKey",
				{
					organizationId: claims.org,
					name: "unauthorized-key",
					environment: "API_KEY_ENVIRONMENT_TEST",
				},
				bobToken,
			),
			await rpc(
				"saas.accounts.v1.WebhookService",
				"CreateSubscription",
				{
					orgId: claims.org,
					url: "https://example.com/hooks/codefly",
					events: ["user.created"],
				},
				bobToken,
			),
		];

		for (const probe of probes) {
			expect(probe.status).toBe(403);
			expect(probe.body.toLowerCase()).toMatch(/admin|owner|permission|denied/);
		}

		// Tenant members may read the roster, proving the read/mutation split.
		const roster = await rpc(
			"saas.accounts.v1.TeamService",
			"ListMembers",
			{ teamId: teamID },
			bobToken,
		);
		expect(roster.status).toBe(200);
	});

	test("BeginOAuth is in the public allowlist (no bearer → 200)", async () => {
		// The whole point of BeginOAuth is to be the entry point of a
		// login flow — it MUST be reachable without auth. Confirms the
		// public-procedure allowlist correctly includes it.
		const r = await rpc("saas.accounts.v1.AuthService", "BeginOAuth", {
			provider: "workos",
			redirectUri: `${FRONTEND}/auth/callback`,
		});
		// 200 = signer + redirect policy wired; 400 = the local placeholder
		// redirect allowlist rejected the URI; 412 = signer not wired. All three
		// prove the public request reached business validation. A 401 would mean
		// policy admission blocked the login entry point.
		expect([200, 400, 412]).toContain(r.status);
		expect(r.body.toLowerCase()).not.toMatch(
			/authentication required|unauthenticated/,
		);
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
		const r = await rpc("saas.accounts.v1.AuthService", "Authenticate", {
			provider: "fake-provider",
			providerId: "nope",
			providerEmail: "nope@example.com",
		});
		// Reaches handler → 4xx with a business error in the body.
		// 401 here would mean the allowlist is wrong.
		expect(r.status).not.toBe(401);
	});
});
