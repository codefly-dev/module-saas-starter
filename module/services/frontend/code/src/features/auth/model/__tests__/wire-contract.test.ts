import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	completePendingClientAuthorization,
	rememberClientAuthorization,
	validateClientAuthorization,
} from "../client-authorization";
import {
	parseOAuthGrant,
	parseOAuthRefusal,
	parseOAuthResolution,
} from "../oauth-authorization";

/**
 * The accounts↔frontend wire contract, from the browser's side.
 *
 * A1007-01: accounts serialises OAuth snake_case and this code used to cast
 * that JSON to a camelCase interface. It compiled, it type-checked, every unit
 * test passed — and `requiresConsent` was `undefined`, which is falsy, which
 * meant consent was SKIPPED and a code was issued for a client the person never
 * approved. The tests that should have caught it instead mocked the camelCase
 * object they wished for.
 *
 * So every fixture below is the real serialised shape: snake_case, exactly the
 * `json:"..."` names in accounts/pkg/adapters/oauth_http.go. The Go side holds
 * the same contract from its end in oauth_wire_contract_test.go.
 */

// The literal bytes accounts sends. Written out rather than built from a
// helper, because a helper shared with the decoder would agree with it by
// construction and prove nothing.
const VALIDATE_RESPONSE = {
	client_name: "Claude Code",
	client_origin: "https://claude.ai",
	client_source: "metadata_document",
	resource: "https://host.example.com/solutions/example/mcp",
	resource_name: "example",
	scope: "offline_access",
	requires_consent: true,
	issuer: "https://host.example.com",
};

const request = {
	responseType: "code",
	clientId: "https://claude.ai/oauth/claude-code-client-metadata",
	redirectUri: "http://localhost:54321/callback",
	state: "opaque-state",
	codeChallenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
	codeChallengeMethod: "S256",
	scope: "offline_access",
	resource: "https://host.example.com/solutions/example/mcp",
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

describe("decoding the real wire shape", () => {
	it("reads the snake_case response accounts actually sends", () => {
		const resolved = parseOAuthResolution(VALIDATE_RESPONSE);
		expect(resolved).not.toBeNull();
		expect(resolved?.requiresConsent).toBe(true);
		expect(resolved?.clientName).toBe("Claude Code");
		expect(resolved?.clientOrigin).toBe("https://claude.ai");
		expect(resolved?.clientSource).toBe("metadata_document");
		expect(resolved?.resourceName).toBe("example");
		expect(resolved?.issuer).toBe("https://host.example.com");
	});

	// The regression in its original form: the camelCase shape this code used to
	// believe in must NOT decode, or a future cast would look like it works.
	it("refuses a camelCase body, which is what the bug looked like", () => {
		expect(
			parseOAuthResolution({
				clientName: "Claude Code",
				clientSource: "metadata_document",
				requiresConsent: true,
			}),
		).toBeNull();
	});

	// Fail closed on anything whose consent requirement cannot be read. A
	// missing or non-boolean `requires_consent` is not "no consent needed".
	it("refuses a response whose consent requirement is unreadable", () => {
		for (const body of [
			null,
			undefined,
			"a string",
			{},
			{ ...VALIDATE_RESPONSE, requires_consent: undefined },
			{ ...VALIDATE_RESPONSE, requires_consent: "true" },
			{ ...VALIDATE_RESPONSE, requires_consent: 1 },
			{ ...VALIDATE_RESPONSE, client_source: "invented" },
			{ ...VALIDATE_RESPONSE, client_origin: "" },
		]) {
			expect(parseOAuthResolution(body), JSON.stringify(body)).toBeNull();
		}
	});

	it("reads the RFC 6749 error object and the grant response", () => {
		const refusal = parseOAuthRefusal({
			error: "invalid_target",
			error_description: "not one this host issues tokens for",
			redirectable: true,
			issuer: "https://host.example.com",
		});
		expect(refusal?.error).toBe("invalid_target");
		expect(refusal?.errorDescription).toBe(
			"not one this host issues tokens for",
		);
		expect(refusal?.redirectable).toBe(true);
		expect(refusal?.issuer).toBe("https://host.example.com");
		// `redirectable` decides whether a browser may be sent to a client's
		// redirect URI, so anything other than true must read as false — a
		// truthy string must not open a redirector.
		expect(
			parseOAuthRefusal({ error: "x", redirectable: "yes" })?.redirectable,
		).toBe(false);

		expect(parseOAuthGrant({ code: "one-time", issuer: "https://h" })).toEqual({
			code: "one-time",
			issuer: "https://h",
		});
		expect(parseOAuthGrant({ expires_in: 60 })).toBeNull();
	});
});

describe("the flow over the real wire shape", () => {
	it("requires consent when the real response says so", async () => {
		vi.mocked(fetch).mockResolvedValue({
			ok: true,
			json: async () => VALIDATE_RESPONSE,
		} as Response);

		const resolution = await validateClientAuthorization(request);
		expect(resolution.requiresConsent).toBe(true);

		rememberClientAuthorization(request, resolution);
		vi.mocked(fetch).mockClear();

		// The assertion the review's cross-language reproducer made and this head
		// now satisfies: no grant request is issued before the person approves.
		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			true,
		);
		expect(fetch).not.toHaveBeenCalled();
		expect(replace).toHaveBeenCalledWith("/oauth2/consent");
	});

	// And it fails closed: a stored resolution that cannot be read sends the
	// person to consent rather than silently granting.
	it("sends the person to consent when the resolution is unreadable", async () => {
		rememberClientAuthorization(request);
		sessionStorage.setItem(
			"client_authorization_resolution",
			JSON.stringify({ clientName: "Claude Code", requiresConsent: false }),
		);

		await expect(completePendingClientAuthorization("token")).resolves.toBe(
			true,
		);
		expect(fetch).not.toHaveBeenCalled();
		expect(replace).toHaveBeenCalledWith("/oauth2/consent");
	});

	it("carries the host's error description, not a generic one", async () => {
		vi.mocked(fetch).mockResolvedValue({
			ok: false,
			status: 400,
			json: async () => ({
				error: "invalid_target",
				error_description:
					"that resource is not one this host issues tokens for",
				redirectable: true,
			}),
		} as Response);

		await expect(validateClientAuthorization(request)).rejects.toThrow(
			/not one this host issues tokens for/,
		);
	});
});
