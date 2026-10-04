import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	CONSENT_PATH,
	completePendingClientAuthorization,
	declineClientAuthorization,
	grantClientAuthorization,
	pendingClientResolution,
	readClientAuthorizationRequest,
	rememberClientAuthorization,
	takeClientAuthorization,
	validateClientAuthorization,
} from "./client-authorization";
import type { OAuthAuthorizationResolution } from "./oauth-authorization";

const request = {
	responseType: "code",
	clientId: "example-addin",
	redirectUri: "https://localhost:3000/auth/callback",
	state: "opaque-state",
	codeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
	codeChallengeMethod: "S256",
	scope: "",
	resource: "",
};

const declared: OAuthAuthorizationResolution = {
	clientName: "Example Add-in",
	clientSource: "registry",
	scope: "offline_access",
	requiresConsent: false,
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

	// The two parameters an MCP client adds. Neither may be invented here: a
	// resource the page supplied rather than received would bind a token's
	// audience to something the client never asked for and the person never saw.
	it("carries the resource indicator and the scope", () => {
		const parsed = readClientAuthorizationRequest(
			new URLSearchParams({
				client_id: "https://claude.ai/oauth/claude-code-client-metadata",
				redirect_uri: "http://localhost:54321/callback",
				code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
				resource: "https://host.example.com/solutions/example/mcp",
				scope: "offline_access",
			}),
		);
		expect(parsed?.resource).toBe(
			"https://host.example.com/solutions/example/mcp",
		);
		expect(parsed?.scope).toBe("offline_access");
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
		vi.mocked(fetch).mockResolvedValue({
			ok: false,
			status: 400,
			json: async () => ({ error: "invalid_client", redirectable: false }),
		} as Response);
		await expect(validateClientAuthorization(request)).rejects.toThrow(
			/not registered/,
		);
	});

	it("returns what the host says about the client and the resource", async () => {
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({
				clientName: "Claude Code",
				clientUri: "https://claude.ai",
				clientSource: "metadata_document",
				resource: "https://host.example.com/solutions/example/mcp",
				resourceName: "example",
				scope: "offline_access",
				requiresConsent: true,
			}),
		} as Response);
		await expect(validateClientAuthorization(request)).resolves.toMatchObject({
			clientName: "Claude Code",
			clientSource: "metadata_document",
			requiresConsent: true,
		});
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

	it("returns the browser to the registered URI with the code, state and issuer", async () => {
		rememberClientAuthorization(request, declared);
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({
				code: "one-time-code",
				issuer: "https://host.example.com",
			}),
		} as Response);

		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			true,
		);
		expect(replace).toHaveBeenCalledWith(
			"https://localhost:3000/auth/callback?code=one-time-code&state=opaque-state&iss=https%3A%2F%2Fhost.example.com",
		);
	});

	// A client the operator never declared, or a request narrowing the token to
	// one resource, is a decision the person makes. The browser goes to the
	// consent page and NO code is issued on the way — a code minted before the
	// approval would make the approval decorative.
	it("sends the person to consent instead of issuing a code", async () => {
		rememberClientAuthorization(request, {
			...declared,
			clientSource: "metadata_document",
			requiresConsent: true,
		});

		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			true,
		);
		// replace, not assign: Back must not return the person to a half-finished
		// sign-in.
		expect(replace).toHaveBeenCalledWith(CONSENT_PATH);
		expect(fetch).not.toHaveBeenCalled();
		// Still pending: the consent page is what completes it.
		expect(takeClientAuthorization()).toEqual(request);
	});

	it("consumes the pending request, so one sign-in yields one code", async () => {
		rememberClientAuthorization(request, declared);
		expect(takeClientAuthorization()).toEqual(request);
		expect(takeClientAuthorization()).toBeNull();
		expect(pendingClientResolution()).toBeNull();
	});

	it("does not move the browser when the host refuses to issue a code", async () => {
		rememberClientAuthorization(request, declared);
		vi.mocked(fetch).mockResolvedValue({ ok: false, status: 403 } as Response);

		await expect(completePendingClientAuthorization("token")).rejects.toThrow(
			/could not be authorized/,
		);
		expect(replace).not.toHaveBeenCalled();
	});

	// A request consumed before the code is in hand is unrecoverable: nothing
	// can retry it, and the client waits on a redirect that never comes.
	it("leaves the request pending when the call fails", async () => {
		rememberClientAuthorization(request, declared);
		vi.mocked(fetch).mockRejectedValue(new Error("network down"));

		await expect(completePendingClientAuthorization("token")).rejects.toThrow();
		expect(takeClientAuthorization()).toEqual(request);
	});

	it("clears the request once the code is in hand", async () => {
		rememberClientAuthorization(request, declared);
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({ code: "one-time-code" }),
		} as Response);

		await completePendingClientAuthorization("token");
		expect(takeClientAuthorization()).toBeNull();
	});
});

describe("grantClientAuthorization", () => {
	it("sends the OAuth parameter names the host validates on", async () => {
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => ({ code: "one-time-code" }),
		} as Response);
		await grantClientAuthorization(
			{
				...request,
				resource: "https://host.example.com/solutions/example/mcp",
			},
			"token",
		);
		const [path, init] = vi.mocked(fetch).mock.calls[0] as [
			string,
			RequestInit,
		];
		expect(path).toBe("/v1/oauth2/authorize/grant");
		expect(JSON.parse(String(init.body))).toMatchObject({
			client_id: "example-addin",
			redirect_uri: "https://localhost:3000/auth/callback",
			code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
			code_challenge_method: "S256",
			resource: "https://host.example.com/solutions/example/mcp",
		});
	});
});

describe("declineClientAuthorization", () => {
	// The client is sitting on a loopback listener. Told `access_denied` it can
	// say so; left waiting it hangs until the person kills it.
	it("tells the client the person declined", () => {
		rememberClientAuthorization(request, declared);
		declineClientAuthorization(request);
		expect(replace).toHaveBeenCalledWith(
			expect.stringContaining("error=access_denied"),
		);
		expect(replace).toHaveBeenCalledWith(
			expect.stringContaining("state=opaque-state"),
		);
		expect(takeClientAuthorization()).toBeNull();
	});
});
