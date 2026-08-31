import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));

// getEndpoints resolves the auth-sidecar gateway base. vi.mock is hoisted
// above module init, so the stub is created with vi.hoisted.
const { getEndpoints } = vi.hoisted(() => ({
	getEndpoints: vi.fn<() => Array<Record<string, unknown>>>(),
}));
vi.mock("codefly", () => ({ getEndpoints }));

import { GET, POST } from "@/app/api/solutions/[id]/proxy/[...path]/route";
import { registerSolution, unregisterSolution } from "@/solutions/registry";

const GATEWAY = "http://gateway.internal:8080";

function withGateway() {
	getEndpoints.mockReturnValue([
		{ service: "auth-sidecar", name: "rest", address: `${GATEWAY}/rest` },
	]);
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

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
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

	it("never leaks internal or hop-by-hop headers to the upstream", async () => {
		withGateway();
		registerAudit();

		await GET(
			proxyRequest("http://frontend/api/solutions/audit/proxy/records", {
				headers: {
					authorization: "Bearer caller-token",
					"x-codefly-internal-token": "cluster-secret",
					"x-secret-smuggle": "leak",
					"x-forwarded-host": "evil.example",
				},
			}),
			context("audit", ["records"]),
		);

		const forwarded = fetchMock.mock.calls[0][1].headers as Headers;
		expect(forwarded.has("x-codefly-internal-token")).toBe(false);
		expect(forwarded.has("x-secret-smuggle")).toBe(false);
		expect(forwarded.has("x-forwarded-host")).toBe(false);
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
	});

	it("categorizes a forwarded solution-upstream failure", async () => {
		withGateway();
		registerAudit();
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
	});
});
