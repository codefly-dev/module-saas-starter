import type { FrontendServiceAllowlistEntry } from "@codefly/saas-plugin-contract";
import { describe, expect, it, vi } from "vitest";

import {
	handlePluginBffRequest,
	type PluginBffDependencies,
	type PluginBffParams,
} from "../../../server/plugin-bff";

const TOKEN = `Bearer ${"a".repeat(32)}`;
const params: PluginBffParams = {
	plugin: "example",
	alias: "api",
	path: ["traffic"],
};
const capabilityParams: PluginBffParams = {
	plugin: "example",
	alias: "api",
	path: [".well-known", "capabilities"],
};

function entry(
	protocol: "connect" | "rest" = "rest",
): FrontendServiceAllowlistEntry {
	return {
		plugin: "example",
		alias: "api",
		protocol,
		routePrefix: "/api/v1/example",
		compatibility: { contract: "example.api", major: 1 },
		target: {
			module: "example-module",
			service: "example-api",
			endpoint: protocol,
		},
	};
}

function request(
	path = "/api/plugins/example/api/traffic",
	init: RequestInit = {},
): Request {
	const headers = new Headers(init.headers);
	if (!headers.has("authorization")) headers.set("authorization", TOKEN);
	return new Request(`https://app.example${path}`, { ...init, headers });
}

function dependencies(
	overrides: Partial<PluginBffDependencies> = {},
): Partial<PluginBffDependencies> {
	return {
		requestID: () => "request-1",
		fetch: vi.fn(async () => new Response("{}")) as typeof fetch,
		resolve: () => ({
			ok: true,
			value: { entry: entry(), baseURL: "http://example-api.internal" },
		}),
		...overrides,
	};
}

async function problemCode(response: Response): Promise<string> {
	return ((await response.json()) as { code: string }).code;
}

describe("generic frontend plugin BFF", () => {
	it("proxies only the allowlisted prefix, query, bearer, and safe headers", async () => {
		const fetchMock = vi.fn(
			async (_target: RequestInfo | URL, _init?: RequestInit) =>
				new Response(JSON.stringify({ calls: 3 }), {
					status: 200,
					headers: {
						"content-type": "application/json",
						"set-cookie": "backend-secret=1",
						"x-request-id": "backend-controlled",
						"x-correlation-id": "backend-correlation",
						"x-upstream-private": "secret",
					},
				}),
		);
		const incoming = request("/api/plugins/example/api/traffic?window=24h", {
			headers: {
				accept: "application/json",
				authorization: TOKEN,
				cookie: "refresh=secret",
				traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
				"x-user-id": "forged-user",
				"x-org-id": "forged-org",
				"x-tenant-id": "forged-tenant",
				"x-role": "super_admin",
				"x-platform-role": "super_admin",
				"x-session-id": "forged-session",
				"x-codefly-gateway-token": "forged-gateway-token",
				"x-codefly-internal-token": "forged-internal-token",
				"x-request-id": "caller-controlled",
				"x-correlation-id": "caller-correlation",
				"request-id": "caller-request",
				forwarded: "for=attacker.example",
				"x-forwarded-host": "attacker.example",
			},
		});

		const response = await handlePluginBffRequest(
			incoming,
			params,
			dependencies({ fetch: fetchMock as typeof fetch }),
		);

		expect(response.status).toBe(200);
		expect(await response.json()).toEqual({ calls: 3 });
		expect(response.headers.get("set-cookie")).toBeNull();
		expect(response.headers.get("x-upstream-private")).toBeNull();
		expect(response.headers.get("x-correlation-id")).toBeNull();
		expect(response.headers.get("cache-control")).toBe("no-store");
		expect(response.headers.get("x-request-id")).toBe("request-1");

		const [target, init] = fetchMock.mock.calls[0];
		expect(String(target)).toBe(
			"http://example-api.internal/api/v1/example/traffic?window=24h",
		);
		const headers = new Headers(init?.headers);
		expect(headers.get("authorization")).toBe(TOKEN);
		expect(headers.get("accept")).toBe("application/json");
		expect(headers.get("x-request-id")).toBe("request-1");
		expect(headers.get("traceparent")).toBe(
			"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		);
		expect(headers.get("cookie")).toBeNull();
		expect(headers.get("x-user-id")).toBeNull();
		expect(headers.get("x-org-id")).toBeNull();
		expect(headers.get("x-tenant-id")).toBeNull();
		expect(headers.get("x-role")).toBeNull();
		expect(headers.get("x-platform-role")).toBeNull();
		expect(headers.get("x-session-id")).toBeNull();
		expect(headers.get("x-codefly-gateway-token")).toBeNull();
		expect(headers.get("x-codefly-internal-token")).toBeNull();
		expect(headers.get("x-correlation-id")).toBeNull();
		expect(headers.get("request-id")).toBeNull();
		expect(headers.get("forwarded")).toBeNull();
		expect(headers.get("x-forwarded-host")).toBeNull();
		expect(init?.redirect).toBe("manual");
	});

	it("creates a fresh host correlation ID for every BFF attempt", async () => {
		const requestID = vi
			.fn()
			.mockReturnValueOnce("request-first")
			.mockReturnValueOnce("request-second");
		const fetchMock = vi.fn(
			async (...args: Parameters<typeof fetch>) => {
				void args;
				return new Response("{}", {
					headers: { "x-request-id": "backend-controlled" },
				});
			},
		);

		for (const expected of ["request-first", "request-second"]) {
			const response = await handlePluginBffRequest(
				request("/api/plugins/example/api/traffic", {
					headers: { "x-request-id": "caller-controlled" },
				}),
				params,
				dependencies({ requestID, fetch: fetchMock as typeof fetch }),
			);
			expect(response.status).toBe(200);
			expect(response.headers.get("x-request-id")).toBe(expected);
		}

		expect(requestID).toHaveBeenCalledTimes(2);
		expect(fetchMock).toHaveBeenCalledTimes(2);
		for (const [index, expected] of [
			[0, "request-first"],
			[1, "request-second"],
		] as const) {
			expect(
				new Headers(fetchMock.mock.calls[index]?.[1]?.headers).get(
					"x-request-id",
				),
			).toBe(expected);
		}
	});

	it("uses one correlation ID in an early problem header and body", async () => {
		const resolve = vi.fn(() => ({
			ok: false as const,
			reason: "not_installed" as const,
		}));
		const response = await handlePluginBffRequest(
			new Request("https://app.example/api/plugins/example/api/traffic"),
			params,
			dependencies({ requestID: () => "request-early", resolve }),
		);

		expect(response.status).toBe(401);
		expect(response.headers.get("x-request-id")).toBe("request-early");
		expect(await response.json()).toMatchObject({
			code: "authentication_required",
			requestId: "request-early",
		});
		expect(resolve).not.toHaveBeenCalled();
	});

	it("drops malformed or zero-valued caller trace context", async () => {
		for (const traceparent of [
			"attacker-controlled",
			"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
			"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		]) {
			const fetchMock = vi.fn(
				async (_target: RequestInfo | URL, _init?: RequestInit) =>
					new Response("{}"),
			);
			const response = await handlePluginBffRequest(
				request("/api/plugins/example/api/traffic", {
					headers: { traceparent },
				}),
				params,
				dependencies({ fetch: fetchMock as typeof fetch }),
			);
			expect(response.status).toBe(200);
			expect(
				new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("traceparent"),
			).toBeNull();
		}
	});

	it("requires a well-formed bearer without trusting the presence cookie", async () => {
		for (const authorization of [
			null,
			"Basic abc",
			"Bearer short",
			"Bearer bad token",
		]) {
			const resolve = vi.fn(() => ({
				ok: true as const,
				value: { entry: entry(), baseURL: "http://example-api.internal" },
			}));
			const fetchMock = vi.fn(async () => new Response("{}"));
			const headers = new Headers({ cookie: "codefly_session=1" });
			if (authorization) headers.set("authorization", authorization);
			const response = await handlePluginBffRequest(
				new Request("https://app.example/api/plugins/example/api/traffic", {
					headers,
				}),
				params,
				dependencies({ resolve, fetch: fetchMock as typeof fetch }),
			);
			expect(response.status).toBe(401);
			expect(await problemCode(response)).toBe("authentication_required");
			expect(resolve).not.toHaveBeenCalled();
			expect(fetchMock).not.toHaveBeenCalled();
		}
	});

	it.each([
		["expired or revoked credential", `Bearer expired.${"a".repeat(24)}`, 401],
		["missing product permission", `Bearer member.${"b".repeat(25)}`, 403],
		["wrong active organization", `Bearer wrong-org.${"c".repeat(22)}`, 403],
		["foreign resource identifier", `Bearer cross-tenant.${"d".repeat(18)}`, 404],
		["explicit support operation", `Bearer support.${"e".repeat(25)}`, 200],
		[
			"explicit super-admin operation",
			`Bearer super-admin.${"f".repeat(21)}`,
			200,
		],
	] as const)(
		"keeps the bearer opaque and preserves the backend decision for %s",
		async (scenario, authorization, upstreamStatus) => {
			void scenario;
			const fetchMock = vi.fn(
				async (...args: Parameters<typeof fetch>) => {
					const headers = new Headers(args[1]?.headers);
					expect(headers.get("authorization")).toBe(authorization);
					for (const trustedHeader of [
						"cookie",
						"forwarded",
						"x-user-id",
						"x-org-id",
						"x-tenant-id",
						"x-role",
						"x-platform-role",
						"x-codefly-gateway-token",
					]) {
						expect(headers.get(trustedHeader)).toBeNull();
					}
					return new Response(upstreamStatus === 200 ? "{}" : null, {
						status: upstreamStatus,
						headers: { "content-type": "application/json" },
					});
				},
			);
			const response = await handlePluginBffRequest(
				request("/api/plugins/example/api/traffic", {
					headers: {
						authorization,
						cookie: "session=forged",
						"x-user-id": "forged-user",
						"x-org-id": "forged-org",
						"x-tenant-id": "forged-tenant",
						"x-platform-role": "super_admin",
					},
				}),
				params,
				dependencies({ fetch: fetchMock as typeof fetch }),
			);

			expect(response.status).toBe(upstreamStatus);
			expect(fetchMock).toHaveBeenCalledTimes(1);
		},
	);

	it("rejects caller-spoofed cross-origin requests", async () => {
		for (const origin of [
			"https://attacker.example",
			"https://app.example.evil",
		]) {
			const incoming = request();
			incoming.headers.set("origin", origin);
			const response = await handlePluginBffRequest(
				incoming,
				params,
				dependencies(),
			);
			expect(response.status).toBe(403);
			expect(await problemCode(response)).toBe("cross_origin_request");
		}
	});

	it("distinguishes unknown aliases and unavailable Codefly endpoints", async () => {
		const missing = await handlePluginBffRequest(
			request(),
			params,
			dependencies({ resolve: () => ({ ok: false, reason: "not_installed" }) }),
		);
		expect(missing.status).toBe(404);
		expect(await problemCode(missing)).toBe("plugin_service_not_found");

		const unavailable = await handlePluginBffRequest(
			request(),
			params,
			dependencies({ resolve: () => ({ ok: false, reason: "unavailable" }) }),
		);
		expect(unavailable.status).toBe(503);
		expect(await problemCode(unavailable)).toBe("backend_unavailable");
	});

	it("probes and normalizes the fixed REST capability response", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return Response.json({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
				capabilities: ["traffic.read", "calls.read"],
			});
		});
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/.well-known/capabilities"),
			capabilityParams,
			dependencies({ fetch: fetchMock as typeof fetch }),
		);

		expect(response.status).toBe(200);
		expect(await response.json()).toEqual({
			schemaVersion: 1,
			contract: "example.api",
			contractMajor: 1,
			capabilities: ["calls.read", "traffic.read"],
		});
		const [target, init] = fetchMock.mock.calls[0];
		expect(String(target)).toBe(
			"http://example-api.internal/.well-known/codefly/frontend-plugin-capabilities",
		);
		expect(init?.method).toBe("GET");
		expect(init?.body).toBeUndefined();
		expect(new Headers(init?.headers).get("authorization")).toBe(TOKEN);
	});

	it("uses the generated Connect capability procedure behind a browser GET", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return Response.json({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
			});
		});
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/.well-known/capabilities"),
			capabilityParams,
			dependencies({
				fetch: fetchMock as typeof fetch,
				resolve: () => ({
					ok: true,
					value: {
						entry: entry("connect"),
						baseURL: "http://example-api.internal",
					},
				}),
			}),
		);

		expect(response.status).toBe(200);
		const [target, init] = fetchMock.mock.calls[0];
		expect(String(target)).toBe(
			"http://example-api.internal/saas.frontend.plugin.v1.FrontendPluginCapabilityService/GetFrontendPluginCapabilities",
		);
		expect(init?.method).toBe("POST");
		expect(init?.body).toBe("{}");
		const headers = new Headers(init?.headers);
		expect(headers.get("content-type")).toBe("application/json");
		expect(headers.get("connect-protocol-version")).toBe("1");
	});

	it.each([
		[
			"wrong contract",
			{
				schemaVersion: 1,
				contract: "other.api",
				contractMajor: 1,
			},
			200,
		],
		[
			"wrong major",
			{
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 2,
			},
			200,
		],
		[
			"unsafe extra data",
			{
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
				privateEndpoint: "http://example-api.internal",
			},
			200,
		],
		["missing capability operation", {}, 404],
	] as const)(
		"contains an incompatible backend with %s",
		async (_name, body, status) => {
			const response = await handlePluginBffRequest(
				request("/api/plugins/example/api/.well-known/capabilities"),
				capabilityParams,
				dependencies({
					fetch: vi.fn(async () =>
						Response.json(body, { status }),
					) as typeof fetch,
				}),
			);

			expect(response.status).toBe(426);
			const text = await response.text();
			expect((JSON.parse(text) as { code: string }).code).toBe(
				"backend_incompatible",
			);
			expect(text).not.toContain("example-api.internal");
		},
	);

	it("keeps a transiently failing capability service unavailable", async () => {
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/.well-known/capabilities"),
			capabilityParams,
			dependencies({
				fetch: vi.fn(
					async () => new Response(null, { status: 503 }),
				) as typeof fetch,
			}),
		);

		expect(response.status).toBe(503);
		expect(await problemCode(response)).toBe("backend_unavailable");
	});

	it("preserves authentication failure from the capability operation", async () => {
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/.well-known/capabilities"),
			capabilityParams,
			dependencies({
				fetch: vi.fn(
					async () =>
						new Response("private authentication detail", { status: 401 }),
				) as typeof fetch,
			}),
		);

		expect(response.status).toBe(401);
		const body = await response.text();
		expect((JSON.parse(body) as { code: string }).code).toBe(
			"authentication_required",
		);
		expect(body).not.toContain("private authentication detail");
	});

	it("derives the method allowlist from the installed protocol", async () => {
		const rest = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", { method: "OPTIONS" }),
			params,
			dependencies(),
		);
		expect(rest.status).toBe(405);
		expect(rest.headers.get("allow")).toBe("GET, POST, PUT, PATCH, DELETE");

		const connect = await handlePluginBffRequest(
			request(),
			params,
			dependencies({
				resolve: () => ({
					ok: true,
					value: {
						entry: entry("connect"),
						baseURL: "http://example-api.internal",
					},
				}),
			}),
		);
		expect(connect.status).toBe(405);
		expect(connect.headers.get("allow")).toBe("POST");
	});

	it.each([
		[[".."], "/api/plugins/example/api/other"],
		[["a/b"], "/api/plugins/example/api/a%2Fb"],
		[["a%2fb"], "/api/plugins/example/api/a%252fb"],
		[["a\\b"], "/api/plugins/example/api/a%5Cb"],
	])("rejects traversal or encoded separators: %j", async (path, url) => {
		const response = await handlePluginBffRequest(
			request(url),
			{ ...params, path },
			dependencies(),
		);
		expect(response.status).toBe(400);
		expect(await problemCode(response)).toBe("invalid_path");
	});

	it("enforces media type and request body limits", async () => {
		const unsupported = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", {
				method: "POST",
				headers: { "content-type": "text/plain" },
				body: "hello",
			}),
			params,
			dependencies(),
		);
		expect(unsupported.status).toBe(415);

		const oversized = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: "1234",
			}),
			params,
			dependencies({ requestLimit: 3 }),
		);
		expect(oversized.status).toBe(413);
		expect(await problemCode(oversized)).toBe("request_too_large");
	});

	it("rejects upstream redirects without exposing their location", async () => {
		const response = await handlePluginBffRequest(
			request(),
			params,
			dependencies({
				fetch: vi.fn(
					async () =>
						new Response(null, {
							status: 302,
							headers: { location: "https://attacker.example/collect" },
						}),
				) as typeof fetch,
			}),
		);
		expect(response.status).toBe(502);
		expect(response.headers.get("location")).toBeNull();
		expect(await problemCode(response)).toBe("upstream_redirect");
	});

	it("bounds upstream responses", async () => {
		const response = await handlePluginBffRequest(
			request(),
			params,
			dependencies({
				responseLimit: 3,
				fetch: vi.fn(async () => new Response("1234")) as typeof fetch,
			}),
		);
		expect(response.status).toBe(502);
		expect(await problemCode(response)).toBe("upstream_response_too_large");
	});

	it("maps timeout, abort, and network failures to stable problems", async () => {
		const waitForAbort = vi.fn(
			async (
				_target: RequestInfo | URL,
				init?: RequestInit,
			): Promise<Response> =>
				new Promise((_resolve, reject) => {
					const signal = init?.signal;
					if (signal?.aborted) reject(signal.reason);
					else
						signal?.addEventListener("abort", () => reject(signal.reason), {
							once: true,
						});
				}),
		);
		const timeout = await handlePluginBffRequest(
			request(),
			params,
			dependencies({ fetch: waitForAbort as typeof fetch, timeoutMs: 5 }),
		);
		expect(timeout.status).toBe(504);
		expect(await problemCode(timeout)).toBe("upstream_timeout");

		const abortController = new AbortController();
		abortController.abort();
		const aborted = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", {
				signal: abortController.signal,
			}),
			params,
			dependencies({ fetch: waitForAbort as typeof fetch }),
		);
		expect(aborted.status).toBe(499);
		expect(await problemCode(aborted)).toBe("client_closed_request");

		const failed = await handlePluginBffRequest(
			request(),
			params,
			dependencies({
				fetch: vi.fn(async () => {
					throw new Error("private network detail");
				}) as typeof fetch,
			}),
		);
		expect(failed.status).toBe(502);
		const failedBody = await failed.text();
		expect((JSON.parse(failedBody) as { code: string }).code).toBe(
			"upstream_failed",
		);
		expect(failedBody).not.toContain("private network detail");
	});
});
