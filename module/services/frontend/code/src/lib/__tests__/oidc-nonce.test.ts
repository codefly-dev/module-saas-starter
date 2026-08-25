import { describe, expect, it } from "vitest";
import type { ProviderPreset } from "@/features/auth/model/types";
import { buildAuthorizeURL, deriveOAuthNonce } from "../auth";

const preset: ProviderPreset = {
	id: "okta",
	displayName: "Okta",
	authorizeURL: "https://idp.example.com/authorize",
	clientID: "client_test",
	scope: "openid email",
};

describe("OIDC nonce", () => {
	// Pinned to the same value as the Go test TestOIDCNonceForState so the
	// browser and the accounts backend derive an identical nonce from a shared
	// state — base64url(sha256(state)). A drift on either side breaks every
	// OIDC login, so both tests must move together.
	it("derives base64url(sha256(state)) matching the backend", async () => {
		expect(await deriveOAuthNonce("state-value")).toBe(
			"prAw7QcteKLKykLonqMhVtJWjsKYigSNm2hM4ecezTs",
		);
	});

	it("derives distinct nonces for distinct states", async () => {
		expect(await deriveOAuthNonce("s1")).not.toBe(await deriveOAuthNonce("s2"));
	});

	it("sends the nonce as the authorize `nonce` parameter", () => {
		const url = new URL(
			buildAuthorizeURL(preset, "https://app/cb", "the-state", "chal", "the-nonce"),
		);
		expect(url.searchParams.get("nonce")).toBe("the-nonce");
	});

	it("omits the nonce parameter when none is given", () => {
		const url = new URL(
			buildAuthorizeURL(preset, "https://app/cb", "the-state", "chal"),
		);
		expect(url.searchParams.has("nonce")).toBe(false);
	});
});
