import type { FrontendServiceAllowlistEntry } from "@codefly-dev/saas-plugin-contract";
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

// A Connect requirement names the services it may reach, because a procedure's
// path carries its own service name and the prefix therefore cannot bound it.
const CONNECT_SERVICES = ["example.records.v1.RecordService"];

function entry(
	protocol: "connect" | "rest" = "rest",
	routePrefix = "/api/v1/example",
	// `undefined` takes the protocol's default; `null` is an entry that declares
	// no services at all, which is the shape the runtime rule has to refuse.
	connectServices?: readonly string[] | null,
): FrontendServiceAllowlistEntry {
	const services =
		connectServices === undefined && protocol === "connect"
			? CONNECT_SERVICES
			: connectServices;
	return {
		plugin: "example",
		alias: "api",
		protocol,
		routePrefix,
		...(services ? { connectServices: services } : {}),
		compatibility: { contract: "example.api", major: 1 },
		target: {
			module: "example-module",
			service: "example-api",
			endpoint: protocol,
		},
	};
}

function resolved(
	protocol: "connect" | "rest" = "rest",
	routePrefix = "/api/v1/example",
	// `undefined` takes the fixture default; `null` is an entry that declares no
	// services at all, which is the shape the runtime rule has to refuse.
	connectServices?: readonly string[] | null,
): Partial<PluginBffDependencies> {
	return {
		resolve: () => ({
			ok: true,
			value: {
				entry: entry(protocol, routePrefix, connectServices),
				baseURL: "http://example-api.internal",
			},
		}),
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
			async (target: RequestInfo | URL, init?: RequestInit) => {
				void target;
				void init;
				return new Response(JSON.stringify({ calls: 3 }), {
					status: 200,
					headers: {
						"content-type": "application/json",
						"set-cookie": "backend-secret=1",
						"x-request-id": "backend-controlled",
						"x-correlation-id": "backend-correlation",
						"x-upstream-private": "secret",
					},
				});
			},
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
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return new Response("{}", {
				headers: { "x-request-id": "backend-controlled" },
			});
		});

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
				async (target: RequestInfo | URL, init?: RequestInit) => {
					void target;
					void init;
					return new Response("{}");
				},
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

	// Both boundaries that forward trace context — this BFF and the solution
	// proxy — now share src/lib/trace-context.ts, so the same header can never
	// get two answers depending on which one a caller happens to reach. The
	// inline check this replaced lowercased the value first, which forwarded an
	// uppercase-hex traceparent the solution proxy dropped; the W3C value is
	// lowercase hex, so the strict reading is the correct one.
	it("drops an uppercase-hex traceparent, as the solution proxy does", async () => {
		const fetchMock = vi.fn(
			async (target: RequestInfo | URL, init?: RequestInit) => {
				void target;
				void init;
				return new Response("{}");
			},
		);
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", {
				headers: {
					traceparent:
						"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
				},
			}),
			params,
			dependencies({ fetch: fetchMock as unknown as typeof fetch }),
		);
		expect(response.status).toBe(200);
		expect(
			new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("traceparent"),
		).toBeNull();
	});

	// The frozen v1 contract forwards traceparent and nothing else of the trace
	// context: tracestate, baggage and vendor trace headers stay behind, which
	// is why this boundary calls parse/serialize rather than forwardTraceContext.
	it("forwards traceparent without tracestate or baggage", async () => {
		const fetchMock = vi.fn(
			async (target: RequestInfo | URL, init?: RequestInit) => {
				void target;
				void init;
				return new Response("{}");
			},
		);
		const traceparent =
			"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/traffic", {
				headers: {
					traceparent,
					tracestate: "vendor=abc",
					baggage: "userId=alice",
				},
			}),
			params,
			dependencies({ fetch: fetchMock as unknown as typeof fetch }),
		);
		expect(response.status).toBe(200);
		const forwarded = new Headers(fetchMock.mock.calls[0]?.[1]?.headers);
		expect(forwarded.get("traceparent")).toBe(traceparent);
		expect(forwarded.has("tracestate")).toBe(false);
		expect(forwarded.has("baggage")).toBe(false);
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
		[
			"foreign resource identifier",
			`Bearer cross-tenant.${"d".repeat(18)}`,
			404,
		],
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
			const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
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
			});
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

	it("uses a validated product-owned REST capability probe path", async () => {
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
						entry: {
							...entry("rest"),
							compatibility: {
								contract: "example.api",
								major: 1,
								probePath: "/api/v1/plugins/example/capabilities",
							},
						},
						baseURL: "http://example-api.internal",
					},
				}),
			}),
		);

		expect(response.status).toBe(200);
		expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
			"http://example-api.internal/api/v1/plugins/example/capabilities",
		);
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

		// On a path the alias actually carries: a connect alias serves procedures,
		// and the path check below runs before the method check, so a REST-shaped
		// path would refuse here for the other reason and pin nothing.
		const connect = await handlePluginBffRequest(
			request(
				"/api/plugins/example/api/example.records.v1.RecordService/Recover",
			),
			{
				...params,
				path: ["example.records.v1.RecordService", "Recover"],
			},
			dependencies(resolved("connect")),
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

	// Finding 4 of the contributed-board review: the transport refused every
	// binding whose last segment carries a colon, so the three task procedures
	// written in Google's custom-verb form were unreachable from a contributed
	// page and a composition had to take a transport from somewhere else.
	it.each([
		[
			["records:recover"],
			"/api/plugins/example/api/records:recover",
			"http://example-api.internal/api/v1/example/records:recover",
		],
		[
			["records", "record-1", "commands:recover"],
			"/api/plugins/example/api/records/record-1/commands:recover",
			"http://example-api.internal/api/v1/example/records/record-1/commands:recover",
		],
		[
			["records", "record-1", "interactions", "interaction-1:respond"],
			"/api/plugins/example/api/records/record-1/interactions/interaction-1:respond",
			"http://example-api.internal/api/v1/example/records/record-1/interactions/interaction-1:respond",
		],
		[
			["triggers", "nightly:fire"],
			"/api/plugins/example/api/triggers/nightly:fire",
			"http://example-api.internal/api/v1/example/triggers/nightly:fire",
		],
	])(
		"forwards the custom-verb route %j under the same bearer as every other call",
		async (path, incoming, expected) => {
			const fetchMock = vi.fn(
				async (target: RequestInfo | URL, init?: RequestInit) => {
					void target;
					void init;
					return new Response(JSON.stringify({ recovered: true }));
				},
			);
			const response = await handlePluginBffRequest(
				request(incoming, {
					method: "POST",
					headers: { "content-type": "application/json" },
					body: JSON.stringify({ idempotency_key: "key-1" }),
				}),
				{ ...params, path },
				dependencies({ fetch: fetchMock as typeof fetch }),
			);

			expect(response.status).toBe(200);
			expect(await response.json()).toEqual({ recovered: true });
			const [target, init] = fetchMock.mock.calls[0];
			// The colon stays literal: percent-encoding it names a different
			// resource, and the binding is only reachable with the separator.
			expect(String(target)).toBe(expected);
			expect(new Headers(init?.headers).get("authorization")).toBe(TOKEN);
			expect(init?.method).toBe("POST");
		},
	);

	it.each([
		[["records:recover", "commands"], "interior colon"],
		[["records:recover:again"], "two colons"],
		[[":recover"], "no resource"],
		[["records:"], "no verb"],
		[["records:Recover"], "upper-case verb"],
	])("refuses a colon outside the custom-verb form (%j, %s)", async (path) => {
		const response = await handlePluginBffRequest(
			request(`/api/plugins/example/api/${path.join("/")}`),
			{ ...params, path },
			dependencies(),
		);
		expect(response.status).toBe(400);
		expect(await problemCode(response)).toBe("invalid_path");
	});

	it("refuses a custom-verb route outside the declared alias as before", async () => {
		const fetchMock = vi.fn(async () => new Response("{}") as Response);
		const response = await handlePluginBffRequest(
			request("/api/plugins/other/api/records:recover", { method: "POST" }),
			{ plugin: "other", alias: "api", path: ["records:recover"] },
			dependencies({
				fetch: fetchMock as typeof fetch,
				resolve: () => ({ ok: false, reason: "not_installed" }),
			}),
		);
		expect(response.status).toBe(404);
		expect(await problemCode(response)).toBe("plugin_service_not_found");
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("carries a connect procedure to the origin root its requirement declares", async () => {
		// The Connect protocol puts a procedure at `/<Service>/<Method>` from the
		// origin, which is where this contract's own capability handshake has
		// always been probed. Until a requirement could declare that root, a
		// Connect backend serving there was unreachable through the transport.
		const fetchMock = vi.fn(
			async (target: RequestInfo | URL, init?: RequestInit) => {
				void target;
				void init;
				return new Response(JSON.stringify({ task: { id: "record-1" } }));
			},
		);
		const response = await handlePluginBffRequest(
			request(
				"/api/plugins/example/api/example.records.v1.RecordService/RecoverRecord",
				{
					method: "POST",
					headers: {
						"content-type": "application/json",
						"connect-protocol-version": "1",
					},
					body: "{}",
				},
			),
			{
				...params,
				path: ["example.records.v1.RecordService", "RecoverRecord"],
			},
			dependencies({
				...resolved("connect", "/"),
				fetch: fetchMock as typeof fetch,
			}),
		);

		expect(response.status).toBe(200);
		expect(await response.json()).toEqual({ task: { id: "record-1" } });
		const [target, init] = fetchMock.mock.calls[0];
		expect(String(target)).toBe(
			"http://example-api.internal/example.records.v1.RecordService/RecoverRecord",
		);
		expect(new Headers(init?.headers).get("connect-protocol-version")).toBe(
			"1",
		);
		expect(new Headers(init?.headers).get("authorization")).toBe(TOKEN);
	});

	it.each([
		[["traffic"], "a REST-shaped path"],
		[["RecordService", "RecoverRecord"], "an unqualified service"],
		[["example.records.v1.RecordService"], "a missing method"],
		[["example.records.v1.RecordService", "records:recover"], "a custom verb"],
	])(
		"refuses %j on a connect alias (%s), which serves procedures only",
		async (path) => {
			const fetchMock = vi.fn(async () => new Response("{}") as Response);
			const response = await handlePluginBffRequest(
				request(`/api/plugins/example/api/${path.join("/")}`, {
					method: "POST",
					headers: { "content-type": "application/json" },
					body: "{}",
				}),
				{ ...params, path },
				dependencies({
					...resolved("connect", "/"),
					fetch: fetchMock as typeof fetch,
				}),
			);
			expect(response.status).toBe(400);
			expect(await problemCode(response)).toBe("invalid_path");
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);

	it.each([
		[
			"a service the requirement never declared",
			["example.secrets.v1.SecretService", "Read"],
		],
		[
			"a service that only looks declared",
			["example.records.v1.RecordServiceX", "Recover"],
		],
	])(
		"refuses a procedure of %s, so a root-prefixed alias is not the whole origin",
		async (_label, path) => {
			// The defect this closes: with `routePrefix: "/"` the FIRST path
			// segment is the service name, chosen by the caller, so a contributed
			// page could POST any `<Service>/<Method>` the upstream origin serves
			// under the viewer's own forwarded bearer. Only the requirement's
			// declared list bounds it.
			const fetchMock = vi.fn(async () => new Response("{}") as Response);
			const response = await handlePluginBffRequest(
				request(`/api/plugins/example/api/${path.join("/")}`, {
					method: "POST",
					headers: { "content-type": "application/json" },
					body: "{}",
				}),
				{ ...params, path },
				dependencies({
					...resolved("connect", "/"),
					fetch: fetchMock as typeof fetch,
				}),
			);
			expect(response.status).toBe(400);
			expect(await problemCode(response)).toBe("invalid_path");
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);

	it.each([
		["a connect alias declaring no services", "connect", "/", null],
		["a connect alias declaring an empty list", "connect", "/", []],
		[
			"a connect alias declaring an unqualified service",
			"connect",
			"/",
			["RecordService"],
		],
		["a REST alias declaring the origin root", "rest", "/", null],
	])(
		"refuses to proxy %s at all",
		async (_label, protocol, routePrefix, connectServices) => {
			// Both rules are the composition's, re-asserted at runtime because the
			// BFF reads the GENERATED allowlist rather than the composition that
			// validated it: a stale or hand-edited projection would otherwise make
			// a Connect alias unbounded, or a REST alias a prefix-less proxy of its
			// whole upstream. It is the alias that cannot be served, not the
			// request, so it is 503 rather than a 4xx the caller could fix.
			const fetchMock = vi.fn(async () => new Response("{}") as Response);
			const response = await handlePluginBffRequest(
				request("/api/plugins/example/api/traffic"),
				params,
				dependencies({
					...resolved(
						protocol as "connect" | "rest",
						routePrefix,
						connectServices as readonly string[] | null,
					),
					fetch: fetchMock as typeof fetch,
				}),
			);
			expect(response.status).toBe(503);
			expect(await problemCode(response)).toBe("backend_unavailable");
			expect(fetchMock).not.toHaveBeenCalled();
		},
	);

	it("carries a connect procedure under the mount prefix its requirement declares", async () => {
		// The non-root Connect prefix: the prefix says where the upstream mounts
		// Connect, the declared list says which of its services the alias speaks
		// to, and the two are independent. #1098 left this shape with no test, and
		// the service-scoped form it replaced unreachable.
		const fetchMock = vi.fn(
			async (target: RequestInfo | URL, init?: RequestInit) => {
				void target;
				void init;
				return new Response(JSON.stringify({ record: { id: "record-1" } }));
			},
		);
		const response = await handlePluginBffRequest(
			request(
				"/api/plugins/example/api/example.records.v1.RecordService/RecoverRecord",
				{
					method: "POST",
					headers: {
						"content-type": "application/json",
						"connect-protocol-version": "1",
					},
					body: "{}",
				},
			),
			{
				...params,
				path: ["example.records.v1.RecordService", "RecoverRecord"],
			},
			dependencies({
				...resolved("connect", "/v1/connect"),
				fetch: fetchMock as typeof fetch,
			}),
		);

		expect(response.status).toBe(200);
		expect(String(fetchMock.mock.calls[0][0])).toBe(
			"http://example-api.internal/v1/connect/example.records.v1.RecordService/RecoverRecord",
		);
	});

	it("refuses an encoded colon in the raw path", async () => {
		// `%3A` joins the separators that fail closed. The colon is forwarded
		// LITERAL on a custom verb precisely because percent-encoding it names a
		// different resource, so a request that spelled `%3A` must not be decoded
		// into the verb form by the framework's catch-all and then admitted as
		// one. Driven through the real route file in
		// src/app/api/plugins/.../__tests__/route.raw-path.test.ts, where the
		// decoding actually happens.
		const fetchMock = vi.fn(async () => new Response("{}") as Response);
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/records%3Arecover", {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: "{}",
			}),
			{ ...params, path: ["records:recover"] },
			dependencies({ fetch: fetchMock as typeof fetch }),
		);
		expect(response.status).toBe(400);
		expect(await problemCode(response)).toBe("invalid_path");
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("refuses a traversal wearing a custom verb", async () => {
		// `..:recover` is neither "." nor "..", so the segment-level not-dot rule
		// let it through while the custom-verb form matched it: the upstream
		// binding would have received ".." as the resource.
		const fetchMock = vi.fn(async () => new Response("{}") as Response);
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/..:recover", {
				method: "POST",
				headers: { "content-type": "application/json" },
				body: "{}",
			}),
			{ ...params, path: ["..:recover"] },
			dependencies({ fetch: fetchMock as typeof fetch }),
		);
		expect(response.status).toBe(400);
		expect(await problemCode(response)).toBe("invalid_path");
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it("still probes the reserved capability path on a root-prefixed connect alias", async () => {
		const fetchMock = vi.fn(async (target: RequestInfo | URL) => {
			void target;
			return Response.json({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
				capabilities: ["records.recover"],
			});
		});
		const response = await handlePluginBffRequest(
			request("/api/plugins/example/api/.well-known/capabilities"),
			capabilityParams,
			dependencies({
				...resolved("connect", "/"),
				fetch: fetchMock as typeof fetch,
			}),
		);

		expect(response.status).toBe(200);
		expect(String(fetchMock.mock.calls[0][0])).toBe(
			"http://example-api.internal/saas.frontend.plugin.v1.FrontendPluginCapabilityService/GetFrontendPluginCapabilities",
		);
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
