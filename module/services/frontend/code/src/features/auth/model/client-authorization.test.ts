import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	completePendingClientAuthorization,
	readClientAuthorizationRequest,
	rememberClientAuthorization,
	takeClientAuthorization,
	validateClientAuthorization,
} from "./client-authorization";

const request = {
	clientId: "example-addin",
	redirectUri: "https://localhost:3000/auth/callback",
	state: "opaque-state",
	codeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
	codeChallengeMethod: "S256",
};

const replace = vi.fn();

beforeEach(() => {
	sessionStorage.clear();
	replace.mockReset();
	vi.stubGlobal("location", { replace });
	vi.stubGlobal("fetch", vi.fn());
});

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("readClientAuthorizationRequest", () => {
	it("reads a client's request out of the login URL", () => {
		const parsed = readClientAuthorizationRequest(
			new URLSearchParams({
				client_id: "example-addin",
				redirect_uri: "https://localhost:3000/auth/callback",
				state: "opaque-state",
				code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
				code_challenge_method: "S256",
			}),
		);
		expect(parsed).toEqual(request);
	});

	// A request with no PKCE challenge is not a request: the code it would earn
	// could be redeemed by whoever intercepted the redirect.
	it("is absent for an ordinary sign-in and for a half-formed request", () => {
		expect(readClientAuthorizationRequest(new URLSearchParams())).toBeNull();
		expect(
			readClientAuthorizationRequest(
				new URLSearchParams({ client_id: "example-addin" }),
			),
		).toBeNull();
		expect(
			readClientAuthorizationRequest(
				new URLSearchParams({
					client_id: "example-addin",
					redirect_uri: "https://localhost:3000/auth/callback",
				}),
			),
		).toBeNull();
	});
});

describe("validateClientAuthorization", () => {
	it("reports the host's refusal rather than proceeding", async () => {
		vi.mocked(fetch).mockResolvedValue({ ok: false, status: 403 } as Response);
		await expect(validateClientAuthorization(request)).rejects.toThrow(
			/not registered/,
		);
	});

	it("falls back to the client id when the host names no client", async () => {
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({}),
		} as Response);
		await expect(validateClientAuthorization(request)).resolves.toBe(
			"example-addin",
		);
	});
});

describe("completePendingClientAuthorization", () => {
	it("does nothing when no client is waiting", async () => {
		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			false,
		);
		expect(fetch).not.toHaveBeenCalled();
		expect(replace).not.toHaveBeenCalled();
	});

	it("returns the browser to the registered URI with the code and state", async () => {
		rememberClientAuthorization(request);
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({ code: "one-time-code" }),
		} as Response);

		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			true,
		);
		expect(replace).toHaveBeenCalledWith(
			"https://localhost:3000/auth/callback?code=one-time-code&state=opaque-state",
		);
	});

	it("consumes the pending request, so one sign-in yields one code", async () => {
		rememberClientAuthorization(request);
		expect(takeClientAuthorization()).toEqual(request);
		expect(takeClientAuthorization()).toBeNull();
	});

	it("does not move the browser when the host refuses to issue a code", async () => {
		rememberClientAuthorization(request);
		vi.mocked(fetch).mockResolvedValue({ ok: false, status: 403 } as Response);

		await expect(completePendingClientAuthorization("token")).rejects.toThrow(
			/could not be authorized/,
		);
		expect(replace).not.toHaveBeenCalled();
	});
});
