import { describe, expect, it, vi } from "vitest";

import { GET, POST } from "@/app/api/plugins/[plugin]/[alias]/[...path]/route";
import { handlePluginBffRequest } from "../../../../../../../../server/plugin-bff";
import type { PluginServiceResolution } from "../../../../../../../../server/plugin-service-bindings";

const BEARER = "Bearer caller-token-abcdef012345";

function context(plugin: string, alias: string, path: string[]) {
	return { params: Promise.resolve({ plugin, alias, path }) };
}

function pluginRequest(
	path: string,
	headers: Record<string, string>,
	init?: RequestInit,
): Request {
	return new Request(`http://localhost:3000/api/plugins/billing/rest/${path}`, {
		headers,
		...init,
	});
}

// happy-dom drops forbidden request headers (Origin, Sec-Fetch-Site, Cookie)
// on Request construction, so the same-origin guard is exercised through a
// request double whose headers carry those values verbatim.
function requestWithForbiddenHeaders(
	url: string,
	headers: Record<string, string>,
): Request {
	const lower = new Map(
		Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]),
	);
	return {
		url,
		method: "GET",
		signal: new AbortController().signal,
		body: null,
		headers: {
			get: (name: string) => lower.get(name.toLowerCase()) ?? null,
			has: (name: string) => lower.has(name.toLowerCase()),
		},
	} as unknown as Request;
}

// A resolution the injected `resolve` returns so the handler reaches the
// upstream fetch. Only the fields the REST path reads (baseURL, protocol,
// routePrefix) matter; the rest of the allowlist entry is cast away.
function okResolution(): PluginServiceResolution {
	return {
		ok: true,
		value: {
			baseURL: "http://plugin-billing.internal:8080",
			entry: {
				protocol: "rest",
				routePrefix: "/plugin/billing",
			} as never,
		},
	};
}

// vi.fn() with no declared signature records an empty args tuple, so the
// upstream call is read back through this typed shape.
type FetchCall = [string | URL, RequestInit];

describe("plugin BFF route — forgery guards (via the real route wrapper)", () => {
	it("rejects a request with no Authorization as 401", async () => {
		const res = await GET(
			pluginRequest("invoices", {}),
			context("billing", "rest", ["invoices"]),
		);
		expect(res.status).toBe(401);
	});

	it("rejects a malformed (non-Bearer) Authorization as 401", async () => {
		const res = await GET(
			pluginRequest("invoices", { authorization: "Basic abc123" }),
			context("billing", "rest", ["invoices"]),
		);
		expect(res.status).toBe(401);
	});

	it("rejects a cross-origin request as 403", async () => {
		const res = await handlePluginBffRequest(
			requestWithForbiddenHeaders(
				"http://localhost:3000/api/plugins/billing/rest/invoices",
				{ authorization: BEARER, origin: "https://evil.example" },
			),
			{ plugin: "billing", alias: "rest", path: ["invoices"] },
		);
		expect(res.status).toBe(403);
	});

	it("rejects a cross-site fetch metadata request as 403", async () => {
		const res = await handlePluginBffRequest(
			requestWithForbiddenHeaders(
				"http://localhost:3000/api/plugins/billing/rest/invoices",
				{ authorization: BEARER, "sec-fetch-site": "cross-site" },
			),
			{ plugin: "billing", alias: "rest", path: ["invoices"] },
		);
		expect(res.status).toBe(403);
	});

	it("rejects a traversal path segment as 400", async () => {
		const res = await GET(
			pluginRequest("..", { authorization: BEARER }),
			context("billing", "rest", [".."]),
		);
		expect(res.status).toBe(400);
	});
});

describe("plugin BFF route — upstream proxying (dependency-injected)", () => {
	it("forwards the caller bearer to the resolved plugin backend", async () => {
		const fetchMock = vi.fn(async () => new Response("ok", { status: 200 }));
		const resolve = vi.fn(() => okResolution());

		const res = await handlePluginBffRequest(
			pluginRequest("invoices", { authorization: BEARER }),
			{ plugin: "billing", alias: "rest", path: ["invoices"] },
			{ resolve, fetch: fetchMock, requestID: () => "req-1" },
		);

		expect(res.status).toBe(200);
		expect(resolve).toHaveBeenCalledWith("billing", "rest");
		const [target, init] = fetchMock.mock.calls[0] as unknown as FetchCall;
		expect(String(target)).toBe(
			"http://plugin-billing.internal:8080/plugin/billing/invoices",
		);
		expect((init.headers as Headers).get("authorization")).toBe(BEARER);
	});

	it("forwards only an allowlisted header set — never arbitrary caller headers", async () => {
		const fetchMock = vi.fn(async () => new Response("ok", { status: 200 }));

		await handlePluginBffRequest(
			pluginRequest("invoices", {
				authorization: BEARER,
				accept: "application/json",
				"x-forwarded-host": "evil.example",
				"x-secret-smuggle": "leak",
			}),
			{ plugin: "billing", alias: "rest", path: ["invoices"] },
			{
				resolve: () => okResolution(),
				fetch: fetchMock,
				requestID: () => "req-1",
			},
		);

		const [, init] = fetchMock.mock.calls[0] as unknown as FetchCall;
		const forwarded = init.headers as Headers;
		expect(forwarded.get("authorization")).toBe(BEARER);
		expect(forwarded.get("accept")).toBe("application/json");
		expect(forwarded.has("x-forwarded-host")).toBe(false);
		expect(forwarded.has("x-secret-smuggle")).toBe(false);
	});

	it("returns 404 when the plugin service is not installed", async () => {
		const fetchMock = vi.fn();

		const res = await handlePluginBffRequest(
			pluginRequest("invoices", { authorization: BEARER }),
			{ plugin: "ghost", alias: "rest", path: ["invoices"] },
			{
				resolve: (): PluginServiceResolution => ({
					ok: false,
					reason: "not_installed",
				}),
				fetch: fetchMock,
				requestID: () => "req-1",
			},
		);

		expect(res.status).toBe(404);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("delegates through the real wrapper and returns a problem+json for an unknown plugin", async () => {
		// No plugin is installed in the test tree, so the real resolver reports
		// the service missing — proving the thin route wrapper awaits its params
		// and hands them to the full handler.
		const res = await POST(
			new Request("http://localhost:3000/api/plugins/billing/rest/invoices", {
				method: "POST",
				headers: { authorization: BEARER, "content-type": "application/json" },
				body: "{}",
			}),
			context("billing", "rest", ["invoices"]),
		);

		expect(res.status).toBe(404);
		expect(res.headers.get("content-type")).toBe("application/problem+json");
		expect(res.headers.get("x-request-id")).toBeTruthy();
	});
});
