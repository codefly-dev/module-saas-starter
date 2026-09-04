import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { proxy } from "@/proxy";

// Stand-in for next.config's build-time snapshot of the env-derived CSP inputs.
const SELF_ONLY_SNAPSHOT = JSON.stringify({
	solutionOrigins: [],
	analyticsOrigin: null,
	turnstile: false,
});

const AUDIT_MANIFEST = "http://localhost:8091/assets/mf-manifest.json";

const AUDIT = {
	id: "audit",
	nav: { title: "Audit", path: "/s/audit" },
	frontend: {
		type: "module-federation",
		manifestUrl: AUDIT_MANIFEST,
		exposedModule: "./Page",
	},
	backend: { serviceAlias: "audit" },
};

function authedRequest(pathname: string): NextRequest {
	const request = new NextRequest(`https://app.example${pathname}`);
	request.cookies.set("codefly_session", "token");
	return request;
}

function directive(csp: string, name: string): string {
	return (
		csp
			.split(";")
			.map((part) => part.trim())
			.find((part) => part === name || part.startsWith(`${name} `)) ?? ""
	);
}

// The registry lives in the route-handler context the proxy cannot share, so
// the proxy reads registered remotes over the loopback solutions listing. Stub
// that boundary and assert the proxy queries the request's own origin.
function stubListing(solutions: unknown[]): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(async (input: URL | RequestInfo) => {
		expect(input.toString()).toBe("https://app.example/api/solutions/register");
		return { ok: true, json: async () => ({ solutions }) } as Response;
	});
	vi.stubGlobal("fetch", fetchMock);
	return fetchMock;
}

describe("proxy solution-page CSP", () => {
	beforeEach(() => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", SELF_ONLY_SNAPSHOT);
	});

	afterEach(() => {
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
	});

	it("allows a registered cross-origin remote without a build-time env", async () => {
		stubListing([AUDIT]);

		const response = await proxy(authedRequest("/s/audit"));
		const csp = response.headers.get("content-security-policy") ?? "";
		const scriptSrc = directive(csp, "script-src");
		// Nonce-based, no unsafe-inline; strict-dynamic + the remote origin.
		expect(scriptSrc).not.toContain("'unsafe-inline'");
		expect(scriptSrc).toMatch(/'nonce-[^']+'/);
		expect(scriptSrc).toContain("'strict-dynamic'");
		expect(scriptSrc).toContain("http://localhost:8091");
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("locks the CSP to self for an unregistered solution id", async () => {
		stubListing([AUDIT]);

		const response = await proxy(authedRequest("/s/unknown"));
		const csp = response.headers.get("content-security-policy") ?? "";
		const scriptSrc = directive(csp, "script-src");
		expect(scriptSrc).not.toContain("'unsafe-inline'");
		expect(scriptSrc).toMatch(/'nonce-[^']+'/);
		expect(scriptSrc).toContain("'strict-dynamic'");
		expect(scriptSrc).not.toContain("localhost");
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
	});

	it("does not query the listing or throw on a malformed id segment", async () => {
		const fetchMock = stubListing([AUDIT]);

		const response = await proxy(authedRequest("/s/%ZZ"));
		const csp = response.headers.get("content-security-policy") ?? "";
		const scriptSrc = directive(csp, "script-src");
		expect(scriptSrc).not.toContain("'unsafe-inline'");
		expect(scriptSrc).toMatch(/'nonce-[^']+'/);
		expect(scriptSrc).toContain("'strict-dynamic'");
		expect(scriptSrc).not.toContain("localhost");
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("stays self-only when the listing is unreachable", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("connection refused");
			}),
		);

		const response = await proxy(authedRequest("/s/audit"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		expect(directive(csp, "script-src")).not.toContain("localhost");
	});

	it("keeps build-time analytics/allowlist hosts from the snapshot, not runtime env", async () => {
		// The snapshot carries the analytics host and a build-time allowlist entry;
		// no NEXT_PUBLIC_* is present in process.env. If the proxy re-read env
		// instead of the snapshot, these would silently drop on solution pages.
		vi.stubEnv(
			"SOLUTION_CSP_INPUTS",
			JSON.stringify({
				solutionOrigins: ["https://trusted.example"],
				analyticsOrigin: "https://eu.i.posthog.com",
				turnstile: false,
			}),
		);
		stubListing([AUDIT]);

		const csp =
			(await proxy(authedRequest("/s/audit"))).headers.get(
				"content-security-policy",
			) ?? "";
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' https://trusted.example http://localhost:8091 https://eu.i.posthog.com",
		);
	});

	it("fails loudly when the build-time snapshot is missing", async () => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", "");
		stubListing([AUDIT]);

		await expect(proxy(authedRequest("/s/audit"))).rejects.toThrow(
			/SOLUTION_CSP_INPUTS/,
		);
	});

	it("sets a per-request nonce'd self CSP on non-solution pages", async () => {
		const fetchMock = stubListing([AUDIT]);

		// The proxy now owns the CSP on every route (next.config emits only the
		// constant hardening headers), so a non-solution page gets a nonce'd
		// self policy — not the old null (which relied on next.config's static CSP).
		const response = await proxy(authedRequest("/settings"));
		const csp = response.headers.get("content-security-policy") ?? "";
		const scriptSrc = directive(csp, "script-src");
		expect(scriptSrc).not.toContain("'unsafe-inline'");
		expect(scriptSrc).toMatch(/'nonce-[^']+'/);
		expect(scriptSrc).toContain("'strict-dynamic'");
		// A non-solution page never queries the listing.
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("does not emit a CSP when redirecting an unauthenticated visitor", async () => {
		stubListing([AUDIT]);

		const response = await proxy(
			new NextRequest("https://app.example/s/audit"),
		);
		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toContain("/auth/login");
		expect(response.headers.get("content-security-policy")).toBeNull();
	});
});
