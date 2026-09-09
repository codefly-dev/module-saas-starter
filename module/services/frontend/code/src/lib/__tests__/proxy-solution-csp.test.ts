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

function authedRequest(url: string): NextRequest {
	const request = new NextRequest(url);
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
// the proxy reads registered remotes over the local solutions listing. Stub
// that boundary and assert the proxy queries loopback at the server's own PORT
// — never a host or port taken from the (client-controlled) request.
function stubListing(
	solutions: unknown[],
	expectedPort = "4711",
): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(async (input: URL | RequestInfo) => {
		expect(input.toString()).toBe(
			`http://127.0.0.1:${expectedPort}/api/solutions/register`,
		);
		return { ok: true, json: async () => ({ solutions }) } as Response;
	});
	vi.stubGlobal("fetch", fetchMock);
	return fetchMock;
}

describe("proxy solution-page CSP", () => {
	beforeEach(() => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", SELF_ONLY_SNAPSHOT);
		// A distinctive non-default port proves the listing target is read from
		// the server's PORT, not hardcoded and not taken from the request.
		vi.stubEnv("PORT", "4711");
		vi.spyOn(console, "error").mockImplementation(() => {});
	});

	afterEach(() => {
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("allows a registered cross-origin remote without a build-time env", async () => {
		stubListing([AUDIT]);

		const response = await proxy(authedRequest("https://app.example/s/audit"));
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

	it("targets loopback at the server PORT regardless of the request Host", async () => {
		// Regression guard: routing the listing fetch through the request origin
		// would let a spoofed Host turn this server-side call into an SSRF sink
		// (and break behind a TLS-terminating ingress). The fetch must stay on
		// 127.0.0.1:$PORT no matter what host the request claims — stubListing
		// asserts the exact target, so an SSRF reintroduction fails here.
		const fetchMock = stubListing([AUDIT]);

		const response = await proxy(
			authedRequest("https://169.254.169.254:1337/s/audit"),
		);

		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(
			directive(
				response.headers.get("content-security-policy") ?? "",
				"connect-src",
			),
		).toBe("connect-src 'self' http://localhost:8091");
	});

	it("defaults the listing port to 3000 when PORT is unset", async () => {
		// Matches Next's standalone server (parseInt(PORT) || 3000), so an empty
		// or missing PORT still hits the port the server actually bound.
		vi.stubEnv("PORT", "");
		stubListing([AUDIT], "3000");

		const response = await proxy(authedRequest("https://app.example/s/audit"));
		expect(
			directive(
				response.headers.get("content-security-policy") ?? "",
				"connect-src",
			),
		).toBe("connect-src 'self' http://localhost:8091");
	});

	it("admits only registered origins — an unknown solution id adds nothing", async () => {
		// The requested id no longer selects the origin: every authenticated
		// document carries the full registered set (see the non-solution-page
		// cases below). What must hold is that nothing beyond the registered
		// origins is ever admitted, whatever id the path names.
		stubListing([AUDIT]);

		const response = await proxy(
			authedRequest("https://app.example/s/unknown"),
		);
		const csp = response.headers.get("content-security-policy") ?? "";
		const scriptSrc = directive(csp, "script-src");
		expect(scriptSrc).not.toContain("'unsafe-inline'");
		expect(scriptSrc).toMatch(/'nonce-[^']+'/);
		expect(scriptSrc).toContain("'strict-dynamic'");
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("does not throw on a malformed path segment", async () => {
		// The path is no longer parsed for an id, so a segment that is not valid
		// percent-encoding must neither throw nor narrow the policy.
		stubListing([AUDIT]);

		const response = await proxy(authedRequest("https://app.example/s/%ZZ"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "script-src")).toMatch(/'nonce-[^']+'/);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("logs and stays self-only when the listing is unreachable", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("connection refused");
			}),
		);

		const response = await proxy(authedRequest("https://app.example/s/audit"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		expect(directive(csp, "script-src")).not.toContain("localhost");
		// The failure is surfaced, not swallowed — a silent fallback is
		// indistinguishable from the bug this fix addresses.
		expect(console.error).toHaveBeenCalledOnce();
	});

	it("logs and stays self-only when the listing responds non-ok", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(
				async () =>
					({ ok: false, status: 502, json: async () => ({}) }) as Response,
			),
		);

		const response = await proxy(authedRequest("https://app.example/s/audit"));
		expect(
			directive(
				response.headers.get("content-security-policy") ?? "",
				"connect-src",
			),
		).toBe("connect-src 'self'");
		expect(console.error).toHaveBeenCalledOnce();
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
			(await proxy(authedRequest("https://app.example/s/audit"))).headers.get(
				"content-security-policy",
			) ?? "";
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' https://trusted.example http://localhost:8091 https://eu.i.posthog.com",
		);
	});

	it("fails loudly when the build-time snapshot is missing", async () => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", "");
		stubListing([AUDIT]);

		await expect(
			proxy(authedRequest("https://app.example/s/audit")),
		).rejects.toThrow(/SOLUTION_CSP_INPUTS/);
	});

	it("admits every registered origin on non-solution pages too (#545)", async () => {
		// A CSP is document-scoped. The sidebar reaches /s/:id through client-side
		// navigation, which keeps the policy of the document the user started in
		// — the dashboard — so that document must already permit the remote, or
		// the manifest fetch is blocked (Module Federation RUNTIME-003) until a
		// hard reload. Widening only /s/:id was exactly that bug. "/" matters
		// most: it is a PUBLIC path yet the signed-in home the login flow lands
		// on, so the policy must follow the session cookie, not the route.
		const fetchMock = stubListing([AUDIT]);

		for (const page of ["https://app.example/", "https://app.example/settings"]) {
			const response = await proxy(authedRequest(page));
			const csp = response.headers.get("content-security-policy") ?? "";
			const scriptSrc = directive(csp, "script-src");
			// The proxy owns the CSP on every route (next.config emits only the
			// constant hardening headers): nonce'd, no unsafe-inline, plus the
			// registered remote so the runtime-loaded remoteEntry is allowed.
			expect(scriptSrc).not.toContain("'unsafe-inline'");
			expect(scriptSrc).toMatch(/'nonce-[^']+'/);
			expect(scriptSrc).toContain("'strict-dynamic'");
			expect(scriptSrc).toContain("http://localhost:8091");
			expect(directive(csp, "connect-src")).toBe(
				"connect-src 'self' http://localhost:8091",
			);
		}
		expect(fetchMock).toHaveBeenCalledTimes(2);
	});

	it("admits every registered solution, deduplicated by origin", async () => {
		const wiki = {
			...AUDIT,
			id: "wiki",
			nav: { title: "Wiki", path: "/s/wiki" },
			frontend: {
				...AUDIT.frontend,
				manifestUrl: "http://localhost:34041/assets/mf-manifest.json",
			},
		};
		// Same origin as AUDIT under a different path: one source expression.
		const auditTwin = {
			...AUDIT,
			id: "audit-twin",
			frontend: {
				...AUDIT.frontend,
				manifestUrl: "http://localhost:8091/twin/mf-manifest.json",
			},
		};
		stubListing([AUDIT, wiki, auditTwin]);

		const response = await proxy(authedRequest("https://app.example/"));
		expect(
			directive(
				response.headers.get("content-security-policy") ?? "",
				"connect-src",
			),
		).toBe("connect-src 'self' http://localhost:8091 http://localhost:34041");
	});

	it("keeps cookieless documents self-only and never queries the listing", async () => {
		// Without a session nothing can navigate into a solution, so there is
		// nothing to widen — and no listing round-trip on anonymous traffic.
		const fetchMock = stubListing([AUDIT]);

		const response = await proxy(new NextRequest("https://app.example/legal/terms"));
		const csp = response.headers.get("content-security-policy") ?? "";
		expect(directive(csp, "script-src")).toMatch(/'nonce-[^']+'/);
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
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
