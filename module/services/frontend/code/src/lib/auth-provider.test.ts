import { describe, expect, it } from "vitest";
import {
	availableProviders,
	buildAuthorizeURL,
	classifyRefreshStatus,
	expiredSessionLoginTarget,
	isHeaderInjectedProvider,
	type ProviderPreset,
} from "./auth";

describe("Codefly identity provider configuration", () => {
	it("keeps fixture identity out of the external-provider UI", () => {
		expect(availableProviders({ provider: "fixture" })).toEqual([]);
	});

	it("recognises header-injected identity without an OAuth provider button", () => {
		// No hosted authorize URL or client id: there is no OAuth button to render.
		expect(availableProviders({ provider: "header-jwt" })).toEqual([]);
		expect(isHeaderInjectedProvider({ provider: "header-jwt" })).toBe(true);
	});

	it("does not treat OAuth or fixture providers as header-injected", () => {
		expect(isHeaderInjectedProvider({ provider: "workos" })).toBe(false);
		expect(isHeaderInjectedProvider({ provider: "fixture" })).toBe(false);
	});

	it("offers no provider when the configuration is empty", () => {
		// The shape a deployed image had when the provider was inlined at build.
		expect(availableProviders({})).toEqual([]);
		expect(availableProviders({ provider: "workos" })).toEqual([]);
	});

	it("builds the selected WorkOS AuthKit provider from generic identity configuration", () => {
		expect(
			availableProviders({
				provider: "workos",
				displayName: "Company login",
				authorizeURL: "https://api.workos.com/user_management/authorize",
				clientID: "client_123",
				authorizeSelector: "authkit",
			}),
		).toEqual([
			{
				id: "workos",
				displayName: "Company login",
				authorizeURL: "https://api.workos.com/user_management/authorize",
				clientID: "client_123",
				scope: "openid profile email",
				authorizeParams: { prompt: "select_account", provider: "authkit" },
			},
		]);
	});

	it("defaults the authorize scope so a minimally-configured provider returns email", () => {
		const [preset] = availableProviders({
			provider: "oidc",
			authorizeURL: "https://idp.example.com/authorize",
			clientID: "client_123",
		});
		// An unset IDENTITY_SCOPE must still yield the standard OIDC scopes —
		// without `openid ... email` the id_token carries no email and accounts
		// rejects the callback with ErrMissingEmail. `groups` stays out (WorkOS
		// AuthKit rejects it as invalid_scope).
		expect(preset.scope).toBe("openid profile email");
		const url = new URL(
			buildAuthorizeURL(
				preset,
				"http://localhost:21931/auth/callback",
				"signed-state",
			),
		);
		expect(url.searchParams.get("scope")).toBe("openid profile email");
	});

	it("lets an explicit scope override the default", () => {
		const [preset] = availableProviders({
			provider: "oidc",
			authorizeURL: "https://idp.example.com/authorize",
			clientID: "client_123",
			scope: "openid email",
		});
		expect(preset.scope).toBe("openid email");
	});

	it("includes provider selectors and PKCE without inventing a scope", () => {
		const preset: ProviderPreset = {
			id: "workos",
			displayName: "WorkOS",
			authorizeURL: "https://api.workos.com/user_management/authorize",
			clientID: "client_123",
			authorizeParams: { provider: "authkit" },
		};
		const url = new URL(
			buildAuthorizeURL(
				preset,
				"http://localhost:21931/auth/callback",
				"signed-state",
				"pkce-challenge",
			),
		);
		expect(url.searchParams.get("provider")).toBe("authkit");
		expect(url.searchParams.get("code_challenge")).toBe("pkce-challenge");
		expect(url.searchParams.has("scope")).toBe(false);
	});
});

describe("expired-session login redirect", () => {
	it("preserves the current location in `next` so sign-in returns here", () => {
		expect(
			expiredSessionLoginTarget({ pathname: "/s/example", search: "" }),
		).toBe("/auth/login?next=%2Fs%2Fexample");
		expect(
			expiredSessionLoginTarget({ pathname: "/settings", search: "?tab=api" }),
		).toBe("/auth/login?next=%2Fsettings%3Ftab%3Dapi");
	});

	it("does not redirect from an auth page, which would loop", () => {
		expect(
			expiredSessionLoginTarget({ pathname: "/auth/login", search: "" }),
		).toBeNull();
		expect(
			expiredSessionLoginTarget({ pathname: "/auth/mfa", search: "" }),
		).toBeNull();
	});
});

describe("refresh status classification", () => {
	it("treats an explicit 401/403 as an expired (gone) session", () => {
		expect(classifyRefreshStatus({ ok: false, status: 401 })).toBe("expired");
		expect(classifyRefreshStatus({ ok: false, status: 403 })).toBe("expired");
	});

	it("treats a 5xx / rate-limit / network failure as transient, not expired", () => {
		// The session is still valid behind a hiccup; classifying these as
		// `expired` would log the user out (and redirect) over an outage.
		expect(classifyRefreshStatus({ ok: false, status: 500 })).toBe(
			"unavailable",
		);
		expect(classifyRefreshStatus({ ok: false, status: 503 })).toBe(
			"unavailable",
		);
		expect(classifyRefreshStatus({ ok: false, status: 429 })).toBe(
			"unavailable",
		);
		expect(classifyRefreshStatus(null)).toBe("unavailable");
	});

	it("treats a 2xx as ok", () => {
		expect(classifyRefreshStatus({ ok: true, status: 200 })).toBe("ok");
	});
});
