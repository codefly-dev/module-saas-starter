import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The proxy resolves the cluster-internal token through the Codefly SDK and
// presents it on the internal detail lookup. vi.mock is hoisted above module
// init, so the stub must be created with vi.hoisted.
const {
	getWorkspaceSecret,
	getCurrentModule,
	getCurrentService,
	getEndpoints,
} = vi.hoisted(() => ({
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
	getCurrentModule: vi.fn<() => string>(),
	getCurrentService: vi.fn<() => string>(),
	getEndpoints: vi.fn<() => unknown[]>(),
}));
vi.mock("codefly", () => ({
	getWorkspaceSecret,
	getCurrentModule,
	getCurrentService,
	getEndpoints,
}));

const INTERNAL_TOKEN = "internal-test-token";

// The proxy keeps module-level state — a short-lived cache of the solutions
// listing and the dedup key for failure logging — so each test imports a fresh
// module graph rather than inheriting the previous test's cache or log state.
let proxy: typeof import("@/proxy").proxy;

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

// A top-level navigation: the only request kind whose CSP a browser enforces.
function documentRequest(url: string): NextRequest {
	return new NextRequest(url, {
		headers: {
			"sec-fetch-dest": "document",
			accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		},
	});
}

function authedDocument(url: string): NextRequest {
	const request = documentRequest(url);
	request.cookies.set("codefly_session", "token");
	return request;
}

// fetch/XHR from an already-loaded document: a Connect RPC, a REST call, or
// Next's RSC payload request. It inherits the document's policy and carries
// none of its own.
function authedSubresource(url: string): NextRequest {
	const request = new NextRequest(url, {
		headers: { "sec-fetch-dest": "empty", accept: "*/*" },
	});
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

function cspOf(response: Response): string {
	return response.headers.get("content-security-policy") ?? "";
}

// The registry lives in the route-handler context the proxy cannot share, so
// the proxy reads registered remotes over the local internal detail lookup.
// Stub that boundary and assert the proxy queries loopback at the server's own
// PORT — never a host or port taken from the (client-controlled) request — and
// authenticates with the cluster-internal token the lookup requires.
function stubListing(
	solutions: unknown[],
	expectedPort = "4711",
): ReturnType<typeof vi.fn> {
	return stubListingBody({ solutions }, expectedPort);
}

function stubListingBody(
	body: unknown,
	expectedPort = "4711",
): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(
		async (input: URL | RequestInfo, init?: RequestInit) => {
			expect(input.toString()).toBe(
				`http://127.0.0.1:${expectedPort}/api/internal/solutions`,
			);
			expect(new Headers(init?.headers).get("x-codefly-internal-token")).toBe(
				INTERNAL_TOKEN,
			);
			return { ok: true, json: async () => body } as Response;
		},
	);
	vi.stubGlobal("fetch", fetchMock);
	return fetchMock;
}

describe("proxy solution CSP", () => {
	// The listing result is cached for a few seconds, so tests that need a
	// second lookup move the clock rather than waiting. Spying on Date.now
	// leaves timers and microtask ordering alone.
	let now = 1_700_000_000_000;
	function advanceClock(ms: number): void {
		now += ms;
	}

	beforeEach(async () => {
		now = 1_700_000_000_000;
		getWorkspaceSecret.mockReturnValue(INTERNAL_TOKEN);
		// Isolated-component default: no Codefly runtime identity to discover.
		getCurrentModule.mockReturnValue("");
		getCurrentService.mockReturnValue("");
		getEndpoints.mockReturnValue([]);
		vi.spyOn(Date, "now").mockImplementation(() => now);
		vi.stubEnv("SOLUTION_CSP_INPUTS", SELF_ONLY_SNAPSHOT);
		// A distinctive non-default port proves the listing target is read from
		// the server's PORT, not hardcoded and not taken from the request.
		vi.stubEnv("PORT", "4711");
		// The product API cases below (/v1/*, /saas.accounts.v1.*) are forwarded
		// to auth-gateway/rest by the proxy, which resolves it per request. A
		// running server always has one — instrumentation.ts refuses to start
		// otherwise — so name it here; without it those requests take the
		// fail-closed 503 path, which carries no CSP to assert on.
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "http://auth-gateway.internal");
		vi.spyOn(console, "error").mockImplementation(() => {});
		vi.spyOn(console, "info").mockImplementation(() => {});
		vi.resetModules();
		({ proxy } = await import("@/proxy"));
	});

	afterEach(() => {
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
		getWorkspaceSecret.mockReset();
		getCurrentModule.mockReset();
		getCurrentService.mockReset();
		getEndpoints.mockReset();
	});

	it("allows a registered cross-origin remote without a build-time env", async () => {
		stubListing([AUDIT]);

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		const csp = cspOf(response);
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
			authedDocument("https://169.254.169.254:1337/s/audit"),
		);

		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("defaults the listing port to 3000 when PORT is unset", async () => {
		// Matches Next's standalone server (parseInt(PORT) || 3000), so an empty
		// or missing PORT still hits the port the server actually bound.
		vi.stubEnv("PORT", "");
		stubListing([AUDIT], "3000");

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("admits only registered origins — an unknown solution id adds nothing", async () => {
		// The requested id does not select the origin: every document carries the
		// full registered set. What must hold is that nothing beyond the
		// registered origins is ever admitted, whatever id the path names.
		stubListing([AUDIT]);

		const response = await proxy(
			authedDocument("https://app.example/s/unknown"),
		);
		const csp = cspOf(response);
		expect(directive(csp, "script-src")).toMatch(/'nonce-[^']+'/);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	it("does not throw on a malformed path segment", async () => {
		// The path is not parsed for an id, so a segment that is not valid
		// percent-encoding must neither throw nor narrow the policy.
		stubListing([AUDIT]);

		const response = await proxy(authedDocument("https://app.example/s/%ZZ"));
		const csp = cspOf(response);
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

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		const csp = cspOf(response);
		expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		expect(directive(csp, "script-src")).not.toContain("localhost");
		// The failure is surfaced, not swallowed — a silent fallback is
		// indistinguishable from the bug this fix addresses.
		expect(console.error).toHaveBeenCalledOnce();
	});

	it("admits a registered origin when the public origin cannot be resolved", async () => {
		// The internal secret is the only thing this lookup needs. Reading it off
		// the gateway context also made it depend on PUBLIC ORIGIN resolution,
		// which fails on a malformed `x-forwarded-host` — silently narrowing the
		// policy to self-only and reporting a missing secret that was present.
		getCurrentModule.mockReturnValue("saas-starter");
		getCurrentService.mockReturnValue("frontend");
		// The render bakes the own HTTP endpoint as a loopback placeholder, so
		// the public origin comes from the request's forwarded host.
		getEndpoints.mockReturnValue([
			{
				module: "saas-starter",
				service: "frontend",
				name: "http",
				protocol: "HTTP",
				address: "http://localhost:8080",
			},
		]);
		const fetchMock = stubListing([AUDIT]);

		const request = new NextRequest("http://10.0.0.5/s/audit", {
			headers: {
				"sec-fetch-dest": "document",
				accept: "text/html",
				"x-forwarded-proto": "https",
				"x-forwarded-host": "app example",
			},
		});
		request.cookies.set("codefly_session", "token");

		const response = await proxy(request);
		expect(directive(cspOf(response), "connect-src")).toBe(
			`connect-src 'self' ${new URL(AUDIT_MANIFEST).origin}`,
		);
		expect(fetchMock).toHaveBeenCalledOnce();
		expect(console.error).not.toHaveBeenCalled();
	});

	it("stays self-only and reports when no internal token is configured", async () => {
		// Without the secret the detail lookup would 401 — and registration
		// itself fails closed, so there is no registered remote to admit. The
		// self-only policy is correct, but the missing secret is still reported
		// rather than hidden behind a policy that merely looks conservative.
		getWorkspaceSecret.mockReturnValue(undefined);
		const fetchMock = stubListing([AUDIT]);

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self'",
		);
		expect(fetchMock).not.toHaveBeenCalled();
		expect(console.error).toHaveBeenCalledOnce();
	});

	it("logs and stays self-only when the lookup rejects the token", async () => {
		// A rotated or mismatched internal token reads as a 401 here. It must
		// narrow the policy, never widen it from a body the lookup did not
		// authorize.
		vi.stubGlobal(
			"fetch",
			vi.fn(
				async () =>
					({
						ok: false,
						status: 401,
						json: async () => ({ error: "unauthorized" }),
					}) as Response,
			),
		);

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self'",
		);
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

		const response = await proxy(authedDocument("https://app.example/s/audit"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self'",
		);
		expect(console.error).toHaveBeenCalledOnce();
	});

	it("keeps build-time analytics/allowlist hosts from the snapshot, not runtime env", async () => {
		// The snapshot carries the analytics host and a build-time allowlist entry;
		// no NEXT_PUBLIC_* is present in process.env. If the proxy re-read env
		// instead of the snapshot, these would silently drop.
		vi.stubEnv(
			"SOLUTION_CSP_INPUTS",
			JSON.stringify({
				solutionOrigins: ["https://trusted.example"],
				analyticsOrigin: "https://eu.i.posthog.com",
				turnstile: false,
			}),
		);
		stubListing([AUDIT]);

		const csp = cspOf(
			await proxy(authedDocument("https://app.example/s/audit")),
		);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' https://trusted.example http://localhost:8091 https://eu.i.posthog.com",
		);
	});

	it("fails loudly when the build-time snapshot is missing", async () => {
		vi.stubEnv("SOLUTION_CSP_INPUTS", "");
		const fetchMock = stubListing([AUDIT]);

		await expect(
			proxy(authedDocument("https://app.example/s/audit")),
		).rejects.toThrow(/SOLUTION_CSP_INPUTS/);
		// The snapshot is read before any network work, so a broken build never
		// spends a loopback round trip to discover it is broken.
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("admits every registered origin on non-solution pages too (#545)", async () => {
		// A CSP is document-scoped. The sidebar reaches /s/:id through client-side
		// navigation, which keeps the policy of the document the user started in
		// — the dashboard — so that document must already permit the remote, or
		// the manifest fetch is blocked (Module Federation RUNTIME-003) until a
		// hard reload. Widening only /s/:id was exactly that bug.
		stubListing([AUDIT]);

		for (const page of [
			"https://app.example/",
			"https://app.example/settings",
		]) {
			const csp = cspOf(await proxy(authedDocument(page)));
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

		const response = await proxy(authedDocument("https://app.example/"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091 http://localhost:34041",
		);
	});

	it("does not emit a CSP when redirecting an unauthenticated visitor", async () => {
		stubListing([AUDIT]);

		const response = await proxy(
			documentRequest("https://app.example/s/audit"),
		);
		expect(response.status).toBe(307);
		expect(response.headers.get("location")).toContain("/auth/login");
		expect(response.headers.get("content-security-policy")).toBeNull();
	});

	// ── The CSP follows the document, not the session cookie ────────────────
	//
	// AuthProvider writes `codefly_session` from the browser AFTER the login
	// response lands (src/lib/auth.tsx), so the login DOCUMENT is always served
	// without it. Fixture login and header-injected login then enter the app
	// with router.push (src/features/auth/ui/login-page.tsx) — a soft
	// navigation that keeps the cookieless document's policy for the rest of the
	// session. Gating the widening on the cookie left exactly those two flows
	// unable to load a remote until a hard reload: the #545 bug, one document
	// further along.
	it("widens a cookieless login document, which the login flow soft-navigates from", async () => {
		stubListing([AUDIT]);

		const csp = cspOf(
			await proxy(documentRequest("https://app.example/auth/login")),
		);
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	// ── Only documents pay for the lookup ───────────────────────────────────

	it("does not query the listing for a backend API call", async () => {
		// /v1/* and /saas.accounts.v1.* carry the session cookie and are matched
		// by this proxy, so keying on the cookie put a second loopback request
		// through this same server on the backend-API hot path.
		const fetchMock = stubListing([AUDIT]);

		for (const url of [
			"https://app.example/v1/users",
			"https://app.example/saas.accounts.v1.UserService/ListUsers",
			"https://app.example/api/solutions/audit/proxy/data",
		]) {
			const csp = cspOf(await proxy(authedSubresource(url)));
			expect(directive(csp, "connect-src")).toBe("connect-src 'self'");
		}
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("does not query the listing for an RSC payload fetch", async () => {
		// Client-side navigation fetches the RSC payload for the new route. It
		// runs under the originating document's policy and carries none itself.
		const fetchMock = stubListing([AUDIT]);

		await proxy(authedSubresource("https://app.example/settings?_rsc=1a2b3"));
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("falls back to Accept when the client sends no Sec-Fetch-Dest", async () => {
		const fetchMock = stubListing([AUDIT]);

		const navigation = new NextRequest("https://app.example/settings", {
			headers: { accept: "text/html,application/xhtml+xml" },
		});
		navigation.cookies.set("codefly_session", "token");
		expect(directive(cspOf(await proxy(navigation)), "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
		expect(fetchMock).toHaveBeenCalledTimes(1);

		const xhr = new NextRequest("https://app.example/v1/users", {
			headers: { accept: "application/json" },
		});
		xhr.cookies.set("codefly_session", "token");
		expect(directive(cspOf(await proxy(xhr)), "connect-src")).toBe(
			"connect-src 'self'",
		);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("never re-enters the lookup for the lookup path itself", async () => {
		// The lookup is served by this same server, so its fetch runs back
		// through this proxy. Nothing may make that request query the lookup
		// again, at any depth.
		const fetchMock = stubListing([AUDIT]);

		await proxy(authedDocument("https://app.example/api/internal/solutions"));
		expect(fetchMock).not.toHaveBeenCalled();
	});

	// ── The lookup is shared, not repeated per request ──────────────────────

	it("fetches the listing once for a burst of documents", async () => {
		const fetchMock = stubListing([AUDIT]);

		// Concurrent documents coalesce onto one in-flight lookup...
		const burst = await Promise.all([
			proxy(authedDocument("https://app.example/")),
			proxy(authedDocument("https://app.example/settings")),
			proxy(authedDocument("https://app.example/s/audit")),
		]);
		// ...and later documents reuse the cached result.
		burst.push(await proxy(authedDocument("https://app.example/reports")));

		expect(fetchMock).toHaveBeenCalledTimes(1);
		for (const response of burst) {
			expect(directive(cspOf(response), "connect-src")).toBe(
				"connect-src 'self' http://localhost:8091",
			);
		}
	});

	it("picks up a newly registered solution once the cached listing expires", async () => {
		const fetchMock = stubListing([AUDIT]);
		await proxy(authedDocument("https://app.example/"));

		const wiki = {
			...AUDIT,
			id: "wiki",
			frontend: {
				...AUDIT.frontend,
				manifestUrl: "http://localhost:34041/assets/mf-manifest.json",
			},
		};
		stubListing([AUDIT, wiki]);
		advanceClock(6_000);

		const csp = cspOf(await proxy(authedDocument("https://app.example/")));
		expect(directive(csp, "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091 http://localhost:34041",
		);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	// ── A sticky failure is reported once, not once per document ────────────

	it("reports a sustained listing outage once, and again after it recovers", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("connection refused");
			}),
		);

		// A wrong PORT or an unreachable listing fails on every request for as
		// long as it lasts. One line per document would bury the log.
		for (let attempt = 0; attempt < 5; attempt++) {
			await proxy(authedDocument("https://app.example/"));
			advanceClock(6_000);
		}
		expect(console.error).toHaveBeenCalledOnce();

		// Recovery is worth a line, and re-arms the report for the next outage.
		stubListing([AUDIT]);
		await proxy(authedDocument("https://app.example/"));
		advanceClock(6_000);
		expect(console.info).toHaveBeenCalledOnce();

		vi.stubGlobal(
			"fetch",
			vi.fn(async () => {
				throw new Error("connection refused");
			}),
		);
		await proxy(authedDocument("https://app.example/"));
		expect(console.error).toHaveBeenCalledTimes(2);
	});

	// ── A bad listing payload degrades, it does not 500 every document ──────

	it("stays self-only when the listing body carries no solutions array", async () => {
		// The listing crosses a process boundary. Anything else bound on
		// 127.0.0.1:$PORT can answer, and an unguarded iteration here would throw
		// out of the proxy — a 500 on every document, not one solution page.
		stubListingBody({ solutions: 5 });

		const response = await proxy(authedDocument("https://app.example/"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self'",
		);
		expect(console.error).toHaveBeenCalledOnce();
	});

	it("stays self-only when the listing body is not an object at all", async () => {
		stubListingBody("not json");

		const response = await proxy(authedDocument("https://app.example/"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self'",
		);
	});

	it("skips an entry with an unusable manifestUrl and admits the rest", async () => {
		stubListing([
			{
				...AUDIT,
				id: "broken",
				frontend: { ...AUDIT.frontend, manifestUrl: "" },
			},
			{
				...AUDIT,
				id: "relative",
				frontend: { ...AUDIT.frontend, manifestUrl: "/assets/mf.json" },
			},
			{
				...AUDIT,
				id: "numeric",
				frontend: { ...AUDIT.frontend, manifestUrl: 42 },
			},
			{ ...AUDIT, id: "no-frontend", frontend: undefined },
			null,
			AUDIT,
		]);

		const response = await proxy(authedDocument("https://app.example/"));
		expect(directive(cspOf(response), "connect-src")).toBe(
			"connect-src 'self' http://localhost:8091",
		);
	});

	// ── The policy cannot silently outgrow the response header buffer ───────

	it("warns when the assembled policy outgrows a reverse-proxy header buffer", async () => {
		// The policy must not be truncated — dropping origins would silently
		// break the solutions they belong to — so an operator has to hear about
		// it while the site is still serving, not as an opaque 502.
		stubListing(
			Array.from({ length: 250 }, (_, index) => ({
				...AUDIT,
				id: `solution-${index}`,
				frontend: {
					...AUDIT.frontend,
					manifestUrl: `https://solution-${index}.solutions.example.com/assets/mf-manifest.json`,
				},
			})),
		);

		const response = await proxy(authedDocument("https://app.example/"));
		// Still complete: the warning is an alarm, not a truncation.
		expect(directive(cspOf(response), "connect-src")).toContain(
			"https://solution-249.solutions.example.com",
		);
		expect(console.error).toHaveBeenCalledWith(
			expect.stringMatching(/policy is \d+ bytes/),
		);
	});
});
