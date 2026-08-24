import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// registry.ts is server-only; the marker package throws when imported outside a
// server component, so neutralize it for the unit test.
vi.mock("server-only", () => ({}));

import { proxy } from "@/proxy";
import { registerSolution, unregisterSolution } from "@/solutions/registry";

// Stand-in for next.config's build-time snapshot of the env-derived CSP inputs.
const SELF_ONLY_SNAPSHOT = JSON.stringify({
	solutionOrigins: [],
	analyticsOrigin: null,
	turnstile: false,
});

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

function registerAudit(manifestUrl: string): void {
	registerSolution({
		id: "audit",
		nav: { title: "Audit", path: "/s/audit" },
		frontend: {
			type: "module-federation",
			manifestUrl,
			exposedModule: "./Page",
		},
		backend: { serviceAlias: "audit" },
	});
}

describe("proxy solution-page CSP", () => {
	beforeEach(() => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", SELF_ONLY_SNAPSHOT);
	});

	afterEach(() => {
		unregisterSolution("audit");
		vi.unstubAllEnvs();
	});

	it("allows a registered cross-origin remote without a build-time env", () => {
		registerAudit("http://localhost:8091/assets/mf-manifest.json");

		const response = proxy(authedRequest("/s/audit"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "script-src")).toBe(
			"script-src 'self' 'unsafe-inline' http://localhost:8091",
		);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("locks the CSP to self for an unregistered solution id", () => {
		const response = proxy(authedRequest("/s/unknown"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "script-src")).toBe(
			"script-src 'self' 'unsafe-inline'",
		);
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
	});

	it("does not throw and stays self-only on a malformed id segment", () => {
		const response = proxy(authedRequest("/s/%ZZ"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "script-src")).toBe(
			"script-src 'self' 'unsafe-inline'",
		);
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
	});

	it("keeps build-time analytics/allowlist hosts from the snapshot, not runtime env", () => {
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
		registerAudit("http://localhost:8091/assets/mf-manifest.json");

		const csp =
			proxy(authedRequest("/s/audit")).headers.get("content-security-policy") ??
			"";
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' https://trusted.example http://localhost:8091 https://eu.i.posthog.com",
		);
	});

	it("fails loudly when the build-time snapshot is missing", () => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", "");
		expect(() => proxy(authedRequest("/s/audit"))).toThrow(
			/SOLUTION_CSP_INPUTS/,
		);
	});

	it("leaves the build-time CSP untouched on non-solution pages", () => {
		const response = proxy(authedRequest("/settings"));
		expect(response.headers.get("content-security-policy")).toBeNull();
	});

	it("does not emit a CSP when redirecting an unauthenticated visitor", () => {
		registerAudit("http://localhost:8091/assets/mf-manifest.json");

		const response = proxy(new NextRequest("https://app.example/s/audit"));
		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toContain("/auth/login");
		expect(response.headers.get("content-security-policy")).toBeNull();
	});
});
