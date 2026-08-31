import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// The route resolves the gateway base (getEndpoints) and the first-party trust
// context (getCurrentModule/getCurrentService/getWorkspaceSecret, via
// resolveCodeflyGatewayContext). vi.mock is hoisted above module init, so the
// stubs are created with vi.hoisted.
const {
	getEndpoints,
	getCurrentModule,
	getCurrentService,
	getWorkspaceSecret,
} = vi.hoisted(() => ({
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(),
	getCurrentModule: vi.fn<() => string>(() => ""),
	getCurrentService: vi.fn<() => string>(() => ""),
	getWorkspaceSecret:
		vi.fn<(name: string, key: string) => string | undefined>(),
}));
vi.mock("codefly", () => ({
	getEndpoints,
	getCurrentModule,
	getCurrentService,
	getWorkspaceSecret,
}));

import { GET, POST } from "@/app/api/solutions/[id]/proxy/[...path]/route";
import { registerSolution, unregisterSolution } from "@/solutions/registry";

const GATEWAY = "http://gateway.internal:8080";
const INTERNAL_TOKEN = "trusted-internal-token";

function withGateway() {
	getEndpoints.mockReturnValue([
		{ service: "auth-sidecar", name: "rest", address: `${GATEWAY}/rest` },
	]);
}

// Resolve the first-party trust context the way a real runtime does: no
// discoverable own endpoint, so resolveCodeflyGatewayContext falls back to the
// request origin for the public origin and reads the internal token from config.
function withTrustContext() {
	getCurrentModule.mockReturnValue("");
	getCurrentService.mockReturnValue("");
	getWorkspaceSecret.mockImplementation((name, key) =>
		name === "internal-auth" && key === "CODEFLY_INTERNAL_TOKEN"
			? INTERNAL_TOKEN
			: undefined,
	);
}

function registerAudit(serviceAlias = "audit-backend") {
	registerSolution({
		id: "audit",
		nav: { title: "Audit", path: "/s/audit" },
		frontend: {
			type: "module-federation",
			manifestUrl: "https://audit.internal/mf-manifest.json",
			exposedModule: "./Page",
		},
		backend: { serviceAlias },
	});
}

function context(id: string, path?: string[]) {
	return { params: Promise.resolve({ id, path }) };
}

function proxyRequest(
	url: string,
	init?: RequestInit & { headers?: Record<string, string> },
): Request {
	return new Request(url, init);
}

// `Cookie`, `Origin`, and `Sec-*` are forbidden headers a fetch Request strips
// (happy-dom enforces this), so model the real Next server request — which does
// carry them — with a Headers-backed object that preserves them.
function rawRequest(
	url: string,
	method: string,
	headers: Record<string, string>,
): Request {
	return { method, url, headers: new Headers(headers) } as unknown as Request;
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
	// resolveCodeflyGatewayContext calls .trim() on the module/service names, so
	// they must always return a string; the trust token defaults to absent.
	getCurrentModule.mockReturnValue("");
	getCurrentService.mockReturnValue("");
	getWorkspaceSecret.mockReturnValue(undefined);
	fetchMock = vi.fn(
		async () =>
			new Response("upstream-body", {
				status: 200,
				headers: { "content-type": "application/json" },
			}),
	);
	vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
	unregisterSolution("audit");
	getEndpoints.mockReset();
	getCurrentModule.mockReset();
	getCurrentService.mockReset();
	getWorkspaceSecret.mockReset();
	vi.unstubAllGlobals();
});

describe("solution proxy passthrough", () => {
	it("forwards the caller bearer to the resolved gateway upstream", async () => {
		withGateway();
		registerAudit();

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
		const [target, init] = fetchMock.mock.calls[0];
		expect(target).toBe(`${GATEWAY}/solutions/audit-backend/records`);
		expect((init.headers as Headers).get("authorization")).toBe(
			"Bearer caller-token",
		);
	});

	it("rejects a cross-site request before reaching any upstream", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				cookie: "codefly_session=1",
				"sec-fetch-site": "cross-site",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("rejects a request whose Origin is a different origin", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		const res = await POST(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "POST", {
				origin: "http://evil.example",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(403);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("allows an explicit same-origin request through", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		const res = await GET(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "GET", {
				authorization: "Bearer caller-token",
				origin: "http://frontend",
				"sec-fetch-site": "same-origin",
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(200);
		expect(fetchMock).toHaveBeenCalledTimes(1);
	});

	it("never leaks arbitrary caller headers to the upstream", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-secret-smuggle": "leak",
					"x-forwarded-host": "app.example",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.has("x-secret-smuggle")).toBe(false);
		// x-forwarded-host is consumed to derive the public origin, never relayed
		// to the upstream as a raw header.
		expect(forwarded.has("x-forwarded-host")).toBe(false);
	});

	it("replaces a caller-supplied internal token with the trusted one", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-codefly-internal-token": "cluster-secret",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.get("x-codefly-internal-token")).toBe(INTERNAL_TOKEN);
	});

	it("attaches the first-party trust headers resolved from config", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-forwarded-proto": "https",
					"x-forwarded-host": "app.example",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.get("x-codefly-internal-token")).toBe(INTERNAL_TOKEN);
		expect(forwarded.get("x-codefly-public-origin")).toBe(
			"https://app.example",
		);
	});

	it("forwards the session cookie so a cookie-authenticated remote is identified", async () => {
		withGateway();
		withTrustContext();
		registerAudit();

		await GET(
			rawRequest("http://frontend/api/solutions/audit/proxy/records", "GET", {
				cookie: "codefly_session=1; codefly_refresh=abc",
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.get("cookie")).toBe(
			"codefly_session=1; codefly_refresh=abc",
		);
	});

	it("omits trust headers when no internal token is configured", async () => {
		withGateway();
		// No withTrustContext(): getWorkspaceSecret returns undefined.
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.has("x-codefly-internal-token")).toBe(false);
		expect(forwarded.has("x-codefly-public-origin")).toBe(false);
	});

	it("percent-encodes each path segment so it cannot smuggle a query or slash", async () => {
		withGateway();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/x", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["a b", "c?d=e"]),
		);

		const [target] = fetchMock.mock.calls[0];
		expect(target).toBe(`${GATEWAY}/solutions/audit-backend/a%20b/c%3Fd%3De`);
	});

	it("appends the original request query string verbatim", async () => {
		withGateway();
		registerAudit();

		await GET(
			proxyRequest(
				"http://frontend/api/solutions/audit/proxy/records?limit=10&cursor=abc",
				{ headers: { authorization: "Bearer caller-token" } },
			),
			context("audit", ["records"]),
		);

		const [target] = fetchMock.mock.calls[0];
		expect(target).toBe(
			`${GATEWAY}/solutions/audit-backend/records?limit=10&cursor=abc`,
		);
	});

	it("rejects an unregistered solution id without reaching any upstream", async () => {
		withGateway();
		// No registration for "ghost".

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/ghost/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("ghost", ["records"]),
		);

		expect(res.status).toBe(404);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("fails with 502 when the gateway endpoint is unresolvable", async () => {
		getEndpoints.mockReturnValue([]);
		registerAudit();

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(502);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("forwards a POST body to the upstream", async () => {
		withGateway();
		registerAudit();

		await POST(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				method: "POST",
				headers: {
					authorization: "Bearer caller-token",
					"content-type": "application/json",
				},
				body: JSON.stringify({ note: "hi" }),
			}),
			context("audit", ["records"]),
		);

		const init = fetchMock.mock.calls[0][1];
		expect(init.method).toBe("POST");
		expect(init.body).toBeInstanceOf(ArrayBuffer);
		expect(new TextDecoder().decode(init.body as ArrayBuffer)).toBe(
			JSON.stringify({ note: "hi" }),
		);
	});

	it("passes the upstream content-type through but not other upstream headers", async () => {
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValueOnce(
			new Response("secret-body", {
				status: 201,
				headers: {
					"content-type": "application/json",
					"set-cookie": "upstream=leak",
					"x-internal-trace": "abc",
				},
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(201);
		expect(res.headers.get("content-type")).toBe("application/json");
		expect(res.headers.get("set-cookie")).toBeNull();
		expect(res.headers.get("x-internal-trace")).toBeNull();
	});

	it("returns 502 when the resolved gateway is unreachable", async () => {
		withGateway();
		registerAudit();
		fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"));

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(502);
		expect(await res.text()).toBe("solution gateway unreachable");
	});

	it("categorizes a forwarded auth failure and correlates it via request id", async () => {
		withGateway();
		registerAudit();
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		fetchMock.mockResolvedValueOnce(
			new Response("unauthorized", {
				status: 401,
				headers: {
					"content-type": "application/json",
					"x-request-id": "req-42",
				},
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		// Status and body pass through untouched; the host adds the category and
		// the correlation id so an operator can tell auth from an upstream fault.
		expect(res.status).toBe(401);
		expect(res.headers.get("x-codefly-solution-error")).toBe("auth");
		expect(res.headers.get("x-request-id")).toBe("req-42");
		// A client/auth 4xx is a warning, never an error-level log line.
		expect(warn).toHaveBeenCalledTimes(1);
		expect(error).not.toHaveBeenCalled();
		warn.mockRestore();
		error.mockRestore();
	});

	it("labels a forwarded 404 as not_found, not a registration miss", async () => {
		// The solution IS registered (it reached upstream); a 404 here is the
		// solution's own API reporting a missing resource, so it must not read as
		// "not_registered" and send an operator chasing a registration bug.
		withGateway();
		registerAudit();
		fetchMock.mockResolvedValueOnce(
			new Response("no such record", {
				status: 404,
				headers: { "content-type": "application/json" },
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records/999", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records", "999"]),
		);

		expect(res.status).toBe(404);
		expect(res.headers.get("x-codefly-solution-error")).toBe("not_found");
	});

	it("categorizes a forwarded solution-upstream failure", async () => {
		withGateway();
		registerAudit();
		const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
		const error = vi.spyOn(console, "error").mockImplementation(() => {});
		fetchMock.mockResolvedValueOnce(
			new Response("boom", {
				status: 503,
				headers: { "content-type": "application/json" },
			}),
		);

		const res = await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: { authorization: "Bearer caller-token" },
			}),
			context("audit", ["records"]),
		);

		expect(res.status).toBe(503);
		expect(res.headers.get("x-codefly-solution-error")).toBe("upstream");
		// A solution 5xx is a real server fault and warrants error-level attention.
		expect(error).toHaveBeenCalledTimes(1);
		expect(warn).not.toHaveBeenCalled();
		warn.mockRestore();
		error.mockRestore();
	});
});
