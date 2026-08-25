import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { proxy, trustedGatewayRequestHeaders } from "@/proxy";

describe("secret return handling", () => {
	// The proxy now sets a per-request nonce'd CSP on every response, built from
	// next.config's build-time snapshot; provide it as the real build does.
	beforeEach(() => {
		vi.stubEnv(
			"SOLUTION_CSP_INPUTS",
			JSON.stringify({
				solutionOrigins: [],
				analyticsOrigin: null,
				turnstile: false,
			}),
		);
	});
	afterEach(() => {
		vi.unstubAllEnvs();
	});

	it("captures an invitation token in an HttpOnly cookie and redacts the URL", () => {
		const token = "a".repeat(43);
		const response = proxy(
			new NextRequest(
				`https://app.example/invitations/accept?token=${token}&campaign=launch`,
			),
		);

		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toBe(
			"https://app.example/invitations/accept?campaign=launch",
		);
		const cookie = response.headers.get("set-cookie") ?? "";
		expect(cookie).toContain("invitation_return_token=");
		expect(cookie).toContain("HttpOnly");
		expect(cookie).toContain("SameSite=strict");
		expect(response.headers.get("referrer-policy")).toBe("no-referrer");
	});

	it("does not persist a malformed token", () => {
		const response = proxy(
			new NextRequest("https://app.example/waitlist/verify?token=too-short"),
		);
		expect(response.headers.get("location")).toBe(
			"https://app.example/waitlist/verify",
		);
		expect(response.headers.get("set-cookie")).toBeNull();
	});

	it.each(["/invitations/accept/api", "/waitlist/verify/api"])(
		"keeps the secret bridge public: %s",
		(path) => {
			const response = proxy(new NextRequest(`https://app.example${path}`));
			expect(response.status).toBe(200);
			expect(response.headers.get("location")).toBeNull();
		},
	);

	it("authenticates the actual frontend origin on proxied API requests", () => {
		const request = new NextRequest(
			"https://app.example/saas.accounts.v1.AuthService/BeginOAuth",
			{
				method: "POST",
				headers: {
					"X-Codefly-Internal-Token": "caller-controlled",
					"X-Codefly-Public-Origin": "https://evil.example",
				},
			},
		);

		const headers = trustedGatewayRequestHeaders(request, {
			internalToken: "internal-test-token",
			publicOrigin: "http://localhost:42152",
		});
		expect(headers?.get("x-codefly-internal-token")).toBe(
			"internal-test-token",
		);
		expect(headers?.get("x-codefly-public-origin")).toBe(
			"http://localhost:42152",
		);
	});

	it("fails closed when the SDK cannot resolve a gateway context", () => {
		const headers = trustedGatewayRequestHeaders(
			new NextRequest(
				"https://app.example/saas.accounts.v1.AuthService/BeginOAuth",
			),
			undefined,
		);
		expect(headers).toBeUndefined();
	});
});
