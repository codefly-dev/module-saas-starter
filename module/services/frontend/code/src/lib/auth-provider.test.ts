import { afterEach, describe, expect, it } from "vitest";
import {
	availableProviders,
	buildAuthorizeURL,
	isHeaderInjectedProvider,
	type ProviderPreset,
} from "./auth";

const identityKeys = [
	"NEXT_PUBLIC_IDENTITY_PROVIDER",
	"NEXT_PUBLIC_IDENTITY_DISPLAY_NAME",
	"NEXT_PUBLIC_IDENTITY_AUTHORIZE_URL",
	"NEXT_PUBLIC_IDENTITY_CLIENT_ID",
	"NEXT_PUBLIC_IDENTITY_SCOPE",
	"NEXT_PUBLIC_IDENTITY_AUTHORIZE_SELECTOR",
] as const;

afterEach(() => {
	for (const key of identityKeys) delete process.env[key];
});

describe("Codefly identity provider configuration", () => {
	it("keeps fixture identity out of the external-provider UI", () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "fixture";
		expect(availableProviders()).toEqual([]);
	});

	it("recognises header-injected identity without an OAuth provider button", () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "header-jwt";
		// No hosted authorize URL or client id: there is no OAuth button to render.
		expect(availableProviders()).toEqual([]);
		expect(isHeaderInjectedProvider()).toBe(true);
	});

	it("does not treat OAuth or fixture providers as header-injected", () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "workos";
		expect(isHeaderInjectedProvider()).toBe(false);
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "fixture";
		expect(isHeaderInjectedProvider()).toBe(false);
	});

	it("builds the selected WorkOS AuthKit provider from generic identity configuration", () => {
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "workos";
		process.env.NEXT_PUBLIC_IDENTITY_DISPLAY_NAME = "Company login";
		process.env.NEXT_PUBLIC_IDENTITY_AUTHORIZE_URL =
			"https://api.workos.com/user_management/authorize";
		process.env.NEXT_PUBLIC_IDENTITY_CLIENT_ID = "client_123";
		process.env.NEXT_PUBLIC_IDENTITY_AUTHORIZE_SELECTOR = "authkit";

		expect(availableProviders()).toEqual([
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
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "oidc";
		process.env.NEXT_PUBLIC_IDENTITY_AUTHORIZE_URL =
			"https://idp.example.com/authorize";
		process.env.NEXT_PUBLIC_IDENTITY_CLIENT_ID = "client_123";

		const [preset] = availableProviders();
		// Unset NEXT_PUBLIC_IDENTITY_SCOPE must still yield the standard OIDC
		// scopes — without `openid ... email` the id_token carries no email and
		// accounts rejects the callback with ErrMissingEmail. `groups` stays out
		// (WorkOS AuthKit rejects it as invalid_scope).
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
		process.env.NEXT_PUBLIC_IDENTITY_PROVIDER = "oidc";
		process.env.NEXT_PUBLIC_IDENTITY_AUTHORIZE_URL =
			"https://idp.example.com/authorize";
		process.env.NEXT_PUBLIC_IDENTITY_CLIENT_ID = "client_123";
		process.env.NEXT_PUBLIC_IDENTITY_SCOPE = "openid email";

		expect(availableProviders()[0].scope).toBe("openid email");
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
