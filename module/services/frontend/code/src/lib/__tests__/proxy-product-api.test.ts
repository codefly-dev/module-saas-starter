import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The product API destination is resolved through the Codefly SDK first —
// the auth-gateway/rest endpoint the running composition injected — and only
// then through PRODUCT_GATEWAY_INTERNAL. Under `codefly test service` the SDK
// really does discover the gateway, so a test that stubs only the variable is
// not hermetic: it passes without a runtime and fails inside one. Own the SDK
// here so each test decides what the composition resolved. vi.mock is hoisted
// above module init, so the stubs must be created with vi.hoisted.
const {
	getWorkspaceConfiguration,
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
	getWorkspaceConfiguration:
		vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", () => ({
	getWorkspaceConfiguration,
	getWorkspaceSecret,
	getCurrentModule,
	getCurrentService,
	getEndpoints,
}));

const MODULE = "saas-starter";

// The proxy keeps module-level state (the listing cache, the dedup flags for
// failure logging), so each test imports a fresh module graph.
let proxy: typeof import("@/proxy").proxy;

// Stand-in for next.config's build-time snapshot of the env-derived CSP inputs.
const SELF_ONLY_SNAPSHOT = JSON.stringify({
	solutionOrigins: [],
	analyticsOrigin: null,
	turnstile: false,
});

const GATEWAY = "http://auth-gateway.internal:8080";

// Next signals a proxy rewrite with this response header — it is exactly what
// `isRewrite` / `getRewrittenUrl` from next/experimental/testing/server read.
function rewrittenTo(response: Response): string | null {
	return response.headers.get("x-middleware-rewrite");
}

// fetch/XHR from an already-loaded document: a REST call or a Connect RPC.
// Deliberately not a document request, so the CSP path does no listing lookup
// and these stay hermetic.
function productRequest(url: string): NextRequest {
	const request = new NextRequest(url, {
		headers: { "sec-fetch-dest": "empty", accept: "*/*" },
	});
	request.cookies.set("codefly_session", "token");
	return request;
}

async function loadProxy(): Promise<void> {
	vi.resetModules();
	({ proxy } = await import("@/proxy"));
}

describe("proxy product API forwarding", () => {
	beforeEach(async () => {
		getCurrentModule.mockReturnValue(MODULE);
		getCurrentService.mockReturnValue("frontend");
		getWorkspaceSecret.mockReturnValue(undefined);
		// No composition-injected gateway: the override is the only address.
		getEndpoints.mockReturnValue([]);
		vi.stubEnv("SOLUTION_CSP_INPUTS", SELF_ONLY_SNAPSHOT);
		vi.stubEnv("PORT", "4711");
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", GATEWAY);
		vi.spyOn(console, "error").mockImplementation(() => {});
		await loadProxy();
	});

	afterEach(() => {
		vi.unstubAllEnvs();
		vi.unstubAllGlobals();
		vi.restoreAllMocks();
	});

	it("forwards the REST namespace to the gateway, path and query intact", async () => {
		const response = await proxy(
			productRequest("https://app.example/v1/users?limit=10&cursor=abc"),
		);

		expect(rewrittenTo(response)).toBe(
			`${GATEWAY}/v1/users?limit=10&cursor=abc`,
		);
	});

	it("forwards a Connect procedure with its service and method intact", async () => {
		// A next.config rewrite needed a hand-written two-segment source here,
		// because a single `:path*` after the package dot does not match the
		// following `/` under Next 16. Forwarding the request's own pathname has
		// no pattern to get wrong.
		const response = await proxy(
			productRequest(
				"https://app.example/saas.accounts.v1.UserService/ListUsers",
			),
		);

		expect(rewrittenTo(response)).toBe(
			`${GATEWAY}/saas.accounts.v1.UserService/ListUsers`,
		);
	});

	it("follows the running composition when the gateway address changes", async () => {
		// The regression guard for the real defect: a next.config rewrite compiles
		// its destination into the build manifest, so an image built outside the
		// module graph carries no product route at all, and one built beside a live
		// graph carries the BUILD host's gateway forever. Resolving per request
		// means the address the server was started with is the address traffic
		// goes to — a build can no longer freeze it.
		const relocated = "http://auth-gateway.other:9090";
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", relocated);
		await loadProxy();

		const response = await proxy(
			productRequest("https://app.example/v1/users"),
		);

		expect(rewrittenTo(response)).toBe(`${relocated}/v1/users`);
	});

	it("prefers the auth-gateway/rest the composition injected over the override", async () => {
		// Inside the module graph the SDK resolves the gateway; the variable is
		// only for a frontend started outside it (the browser suite's own server).
		getEndpoints.mockReturnValue([
			{
				module: MODULE,
				service: "auth-gateway",
				name: "rest",
				protocol: "REST",
				address: "http://127.0.0.1:50187",
			},
		]);
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "http://auth-gateway.other:9090");
		await loadProxy();

		const response = await proxy(
			productRequest("https://app.example/v1/users"),
		);

		expect(rewrittenTo(response)).toBe("http://127.0.0.1:50187/v1/users");
	});

	it("preserves a base path carried by the gateway address", async () => {
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "http://auth-gateway.internal/base");
		await loadProxy();

		const response = await proxy(
			productRequest("https://app.example/v1/users"),
		);

		expect(rewrittenTo(response)).toBe(
			"http://auth-gateway.internal/base/v1/users",
		);
	});

	it("fails a product API request closed when no gateway resolves", async () => {
		// Never a pass-through: without a destination these paths fall through to
		// this app, which answers a product API call with a 404 page — that reads
		// to the browser as an empty result rather than as the misconfiguration it
		// is. It must be an explicit 503.
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "");
		await loadProxy();

		const response = await proxy(
			productRequest("https://app.example/v1/users"),
		);

		expect(response.status).toBe(503);
		expect(rewrittenTo(response)).toBeNull();
		await expect(response.json()).resolves.toEqual({
			error: "product API gateway unavailable",
		});
	});

	it("refuses to serve Connect procedures from this app either", async () => {
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "");
		await loadProxy();

		const response = await proxy(
			productRequest(
				"https://app.example/saas.accounts.v1.UserService/ListUsers",
			),
		);

		expect(response.status).toBe(503);
		expect(rewrittenTo(response)).toBeNull();
	});

	it("leaves non-product paths as pass-throughs", async () => {
		// Only the two product API namespaces are forwarded; this app still serves
		// its own routes.
		const response = await proxy(
			productRequest("https://app.example/api/openapi"),
		);

		expect(rewrittenTo(response)).toBeNull();
		expect(response.status).toBe(200);
	});

	it("reports a missing gateway once rather than once per request", async () => {
		vi.stubEnv("PRODUCT_GATEWAY_INTERNAL", "");
		await loadProxy();
		const reported = vi.spyOn(console, "error").mockImplementation(() => {});

		await proxy(productRequest("https://app.example/v1/users"));
		await proxy(productRequest("https://app.example/v1/organizations"));

		expect(reported).toHaveBeenCalledTimes(1);
	});
});
