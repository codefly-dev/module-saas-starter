import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("next/server", () => ({ connection: vi.fn(async () => undefined) }));

import { readIdentityConfig } from "./identity-config";

const prefix = "CODEFLY__WORKSPACE_CONFIGURATION__IDENTITY__";
const keys = [
	"IDENTITY_PROVIDER",
	"IDENTITY_AUTHORIZE_URL",
	"IDENTITY_CLIENT_ID",
	"IDENTITY_DISPLAY_NAME",
	"IDENTITY_SCOPE",
	"IDENTITY_AUTHORIZE_SELECTOR",
	"IDENTITY_CLIENT_SECRET",
	"IDENTITY_ISSUER",
];

afterEach(() => {
	vi.unstubAllGlobals();
	for (const key of keys) {
		delete process.env[`${prefix}${key}`];
		delete process.env[
			`CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__${key}`
		];
	}
});

describe("readIdentityConfig", () => {
	it("reads the identity group from the running process", async () => {
		process.env[`${prefix}IDENTITY_PROVIDER`] = "workos";
		process.env[`${prefix}IDENTITY_AUTHORIZE_URL`] =
			"https://api.workos.com/user_management/authorize";
		process.env[`${prefix}IDENTITY_CLIENT_ID`] = "client_123";
		process.env[`${prefix}IDENTITY_DISPLAY_NAME`] = "Company login";
		process.env[`${prefix}IDENTITY_AUTHORIZE_SELECTOR`] = "authkit";

		expect(await readIdentityConfig()).toEqual({
			provider: "workos",
			authorizeURL: "https://api.workos.com/user_management/authorize",
			clientID: "client_123",
			displayName: "Company login",
			scope: undefined,
			authorizeSelector: "authkit",
		});
	});

	it("never carries a secret of the group", async () => {
		process.env.CODEFLY__WORKSPACE_SECRET_CONFIGURATION__IDENTITY__IDENTITY_CLIENT_SECRET =
			"must-not-leak";
		expect(JSON.stringify(await readIdentityConfig())).not.toContain(
			"must-not-leak",
		);
	});

	it("discovers the authorize endpoint from the issuer when none is configured", async () => {
		const fetchMock = vi.fn(async () =>
			Response.json({
				authorization_endpoint: "https://idp.example.test/oauth2/authorize",
			}),
		);
		vi.stubGlobal("fetch", fetchMock);
		process.env[`${prefix}IDENTITY_PROVIDER`] = "workos";
		process.env[`${prefix}IDENTITY_ISSUER`] = "https://idp.example.test/";

		expect((await readIdentityConfig()).authorizeURL).toBe(
			"https://idp.example.test/oauth2/authorize",
		);
		expect(fetchMock).toHaveBeenCalledWith(
			"https://idp.example.test/.well-known/openid-configuration",
			expect.anything(),
		);
		// Cached per issuer: a second request does not rediscover.
		await readIdentityConfig();
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("prefers a configured authorize endpoint and never fetches", async () => {
		const fetchMock = vi.fn();
		vi.stubGlobal("fetch", fetchMock);
		process.env[`${prefix}IDENTITY_ISSUER`] = "https://override.example.test";
		process.env[`${prefix}IDENTITY_AUTHORIZE_URL`] =
			"https://override.example.test/authorize";

		expect((await readIdentityConfig()).authorizeURL).toBe(
			"https://override.example.test/authorize",
		);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("offers no endpoint when discovery fails, and retries on the next request", async () => {
		vi.spyOn(console, "error").mockImplementation(() => {});
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(new Response("down", { status: 503 }))
			.mockResolvedValueOnce(
				Response.json({
					authorization_endpoint: "https://flaky.example.test/authorize",
				}),
			);
		vi.stubGlobal("fetch", fetchMock);
		process.env[`${prefix}IDENTITY_ISSUER`] = "https://flaky.example.test";

		expect((await readIdentityConfig()).authorizeURL).toBeUndefined();
		expect((await readIdentityConfig()).authorizeURL).toBe(
			"https://flaky.example.test/authorize",
		);
	});

	it("refuses a discovered endpoint that is not https", async () => {
		vi.spyOn(console, "error").mockImplementation(() => {});
		vi.stubGlobal(
			"fetch",
			vi.fn(async () =>
				Response.json({
					authorization_endpoint: "http://plain.example.test/a",
				}),
			),
		);
		process.env[`${prefix}IDENTITY_ISSUER`] = "https://plain.example.test";

		expect((await readIdentityConfig()).authorizeURL).toBeUndefined();
	});
});
