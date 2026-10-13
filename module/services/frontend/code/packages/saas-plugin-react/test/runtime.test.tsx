import { cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
	createPluginRuntime,
	PluginRuntimeProvider,
	PluginTransportConfigurationError,
	usePluginService,
} from "../src/runtime.js";

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

function expectConfigurationError(
	operation: () => unknown,
	code: PluginTransportConfigurationError["code"],
): void {
	try {
		operation();
		throw new Error("Expected a plugin transport configuration error");
	} catch (error) {
		expect(error).toBeInstanceOf(PluginTransportConfigurationError);
		expect((error as PluginTransportConfigurationError).code).toBe(code);
	}
}

describe("public plugin React runtime", () => {
	it("uses the latest host token after tenant changes without synthesizing context", async () => {
		let token: string | null = "first-token";
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return new Response("{}");
		});
		const runtime = createPluginRuntime({
			getAccessToken: () => token,
			fetch: fetchMock as typeof fetch,
		});
		const service = runtime.service("example", "api");

		expect(runtime.service("example", "api")).toBe(service);
		expect(Object.keys(runtime)).toEqual(["service"]);
		await service.request("traffic");
		token = "refreshed-token";
		await service.request("traffic", {
			headers: { accept: "application/json" },
			query: { window: "24h", empty: undefined, page: 2 },
		});

		expect(
			new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization"),
		).toBe("Bearer first-token");
		const [url, init] = fetchMock.mock.calls[1];
		expect(url).toBe("/api/plugins/example/api/traffic?page=2&window=24h");
		const headers = new Headers(init?.headers);
		expect(headers.get("authorization")).toBe("Bearer refreshed-token");
		expect(headers.get("accept")).toBe("application/json");
		expect(headers.get("cookie")).toBeNull();
		expect(headers.get("x-org-id")).toBeNull();
		expect(headers.get("x-tenant-id")).toBeNull();
		expect(init?.credentials).toBe("omit");
		expect(init?.cache).toBe("no-store");
		expect(init?.mode).toBe("same-origin");
		expect(init?.redirect).toBe("error");
	});

	it("normalizes URLSearchParams in stable key order", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return new Response("{}");
		});
		const service = createPluginRuntime({
			getAccessToken: () => null,
			fetch: fetchMock as typeof fetch,
		}).service("example", "api");

		await service.request("traffic", {
			query: new URLSearchParams("window=24h&page=2"),
		});

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/api/plugins/example/api/traffic?page=2&window=24h",
		);
	});

	it("leaves authentication absent when the host has no session", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return new Response(null, { status: 401 });
		});
		const service = createPluginRuntime({
			getAccessToken: () => null,
			fetch: fetchMock as typeof fetch,
		}).service("example", "api");

		await service.request(["traffic", "summary"]);
		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/api/plugins/example/api/traffic/summary",
		);
		expect(
			new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization"),
		).toBeNull();
	});

	it("loads and caches the normalized backend capability handshake", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return Response.json({
				schemaVersion: 1,
				contract: "example.api",
				contractMajor: 1,
				capabilities: ["traffic.read"],
			});
		});
		const service = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: fetchMock as typeof fetch,
		}).service("example", "api");

		const first = await service.capabilities();
		const second = await service.capabilities();
		expect(first).toBe(second);
		expect(first).toMatchObject({
			schemaVersion: 1,
			contract: "example.api",
			contractMajor: 1,
			capabilities: ["traffic.read"],
		});
		expect(fetchMock).toHaveBeenCalledTimes(1);
		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/api/plugins/example/api/.well-known/capabilities",
		);
		expect(
			new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization"),
		).toBe("Bearer host-token");
	});

	it("maps an invalid successful handshake to incompatible", async () => {
		const service = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: vi.fn(async () =>
				Response.json(
					{
						schemaVersion: 1,
						contract: "unsafe contract",
						contractMajor: 1,
					},
					{ headers: { "x-request-id": "request-1" } },
				),
			) as typeof fetch,
		}).service("example", "api");

		await expect(service.capabilities()).rejects.toMatchObject({
			failure: {
				state: "incompatible",
				code: "backend_incompatible",
				requestId: "request-1",
			},
		});
	});

	it("retries a failed capability handshake instead of caching failure", async () => {
		const fetchMock = vi
			.fn()
			.mockResolvedValueOnce(
				Response.json(
					{ code: "backend_unavailable" },
					{
						status: 503,
						headers: { "content-type": "application/problem+json" },
					},
				),
			)
			.mockResolvedValueOnce(
				Response.json({
					schemaVersion: 1,
					contract: "example.api",
					contractMajor: 1,
				}),
			);
		const service = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: fetchMock as typeof fetch,
		}).service("example", "api");

		await expect(service.capabilities()).rejects.toMatchObject({
			failure: { state: "unavailable", code: "backend_unavailable" },
		});
		await expect(service.capabilities()).resolves.toMatchObject({
			contract: "example.api",
		});
		expect(fetchMock).toHaveBeenCalledTimes(2);
	});

	it.each([
		"authorization",
		"cookie",
		"host",
		"origin",
		"forwarded",
		"x-user-id",
		"x-org-id",
		"x-tenant-id",
		"x-codefly-gateway-token",
		"x-request-id",
		"x-correlation-id",
		"x-forwarded-host",
	])("rejects product control of trusted header '%s'", async (header) => {
		const service = createPluginRuntime({
			getAccessToken: () => "host-token",
		}).service("example", "api");
		await expect(
			service.request("traffic", { headers: { [header]: "forged" } }),
		).rejects.toMatchObject({
			code: "forbidden_header",
		});
	});

	it.each([
		"",
		"/absolute",
		"https://backend.example/traffic",
		"../private",
		"traffic//summary",
		"traffic%2fprivate",
		"traffic?target=other",
		"traffic#fragment",
		// A colon separates a custom verb from its resource, so it is carried on
		// the last segment and refused anywhere else.
		"records:recover/commands",
		"records:recover:again",
		":recover",
		"records:",
		"records:Recover",
		// The resource half of a custom verb is a segment like any other: a
		// traversal wearing a verb would bind ".." as the resource upstream.
		"..:recover",
		"records/..:recover",
		".:recover",
	])("rejects an unsafe product path '%s'", async (path) => {
		const service = createPluginRuntime({ getAccessToken: () => null }).service(
			"example",
			"api",
		);
		await expect(service.request(path)).rejects.toMatchObject({
			code: "invalid_path",
		});
	});

	it.each([
		["records:recover", "/api/plugins/example/api/records:recover"],
		[
			"records/record-1/commands:recover",
			"/api/plugins/example/api/records/record-1/commands:recover",
		],
		[
			"records/record-1/interactions/interaction-1:respond",
			"/api/plugins/example/api/records/record-1/interactions/interaction-1:respond",
		],
		["triggers/nightly:fire", "/api/plugins/example/api/triggers/nightly:fire"],
	])(
		"carries the custom-verb route '%s' with its colon literal",
		async (path, expected) => {
			// `encodeURIComponent` would spell the colon `%3A`, which is a
			// different resource: the custom-verb form is only reachable with the
			// separator literal.
			const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
				void args;
				return Response.json({ ok: true });
			});
			const service = createPluginRuntime({
				getAccessToken: () => "host-token",
				fetch: fetchMock as typeof fetch,
			}).service("example", "api");

			const response = await service.request(path, { method: "POST" });

			expect(await response.json()).toEqual({ ok: true });
			expect(fetchMock.mock.calls[0]?.[0]).toBe(expected);
			expect(
				new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization"),
			).toBe("Bearer host-token");
		},
	);

	it("carries a connect procedure as two safe segments", async () => {
		const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
			void args;
			return Response.json({ ok: true });
		});
		const service = createPluginRuntime({
			getAccessToken: () => "host-token",
			fetch: fetchMock as typeof fetch,
		}).service("example", "procedures");

		await service.request("example.records.v1.RecordService/RecoverRecord", {
			method: "POST",
		});

		expect(fetchMock.mock.calls[0]?.[0]).toBe(
			"/api/plugins/example/procedures/example.records.v1.RecordService/RecoverRecord",
		);
	});

	describe("a connect alias the runtime was told about", () => {
		// The host's own Connect rule, run in the browser. The BFF refuses a
		// procedure outside the declared services either way; running the same
		// rule here means the two halves cannot disagree about a shape, which is
		// the whole reason the rule lives in one package. The requirement carries
		// no address or credential — only what the plugin declared.
		const services = [
			{
				plugin: "example",
				alias: "procedures",
				protocol: "connect" as const,
				routePrefix: "/",
				connectServices: ["example.records.v1.RecordService"],
				compatibility: { contract: "example.api", major: 1 },
			},
		];

		function connectService(fetchMock?: typeof fetch) {
			return createPluginRuntime({
				getAccessToken: () => "host-token",
				services,
				...(fetchMock ? { fetch: fetchMock } : {}),
			}).service("example", "procedures");
		}

		it("carries a procedure of a declared service", async () => {
			const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
				void args;
				return Response.json({ ok: true });
			});

			await connectService(fetchMock as typeof fetch).request(
				"example.records.v1.RecordService/RecoverRecord",
				{ method: "POST" },
			);

			expect(fetchMock.mock.calls[0]?.[0]).toBe(
				"/api/plugins/example/procedures/example.records.v1.RecordService/RecoverRecord",
			);
		});

		it.each([
			[
				"a service the requirement never declared",
				"example.secrets.v1.SecretService/Read",
			],
			["a REST-shaped path", "records/record-1"],
			["a method with no service", "RecoverRecord"],
		])(
			"refuses %s before a request leaves the browser",
			async (_label, path) => {
				const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
					void args;
					return Response.json({ ok: true });
				});

				await expect(
					connectService(fetchMock as typeof fetch).request(path, {
						method: "POST",
					}),
				).rejects.toMatchObject({ code: "invalid_path" });
				expect(fetchMock).not.toHaveBeenCalled();
			},
		);

		it("still probes the reserved capability path", async () => {
			// The handshake is this contract's own, at a fixed path the host
			// translates into the protocol's procedure. It is not caller-chosen,
			// so the declared list does not bound it.
			const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
				void args;
				return Response.json({
					schemaVersion: 1,
					contract: "example.api",
					contractMajor: 1,
					capabilities: ["records.recover"],
				});
			});

			await connectService(fetchMock as typeof fetch).capabilities();

			expect(fetchMock.mock.calls[0]?.[0]).toBe(
				"/api/plugins/example/procedures/.well-known/capabilities",
			);
		});

		it("leaves the host the authority when it was told nothing", async () => {
			// A runtime created without the installed requirements applies the
			// generic path rule alone, as before: a client that guessed would
			// refuse routes a composition had legitimately installed.
			const fetchMock = vi.fn(async (...args: Parameters<typeof fetch>) => {
				void args;
				return Response.json({ ok: true });
			});
			const service = createPluginRuntime({
				getAccessToken: () => "host-token",
				fetch: fetchMock as typeof fetch,
			}).service("example", "procedures");

			await service.request("records/record-1", { method: "POST" });

			expect(fetchMock.mock.calls[0]?.[0]).toBe(
				"/api/plugins/example/procedures/records/record-1",
			);
		});
	});

	it("rejects unsafe plugin and alias identifiers before creating a transport", () => {
		const runtime = createPluginRuntime({ getAccessToken: () => null });
		expectConfigurationError(
			() => runtime.service("../product", "api"),
			"invalid_plugin",
		);
		expectConfigurationError(
			() => runtime.service("example", "api/path"),
			"invalid_alias",
		);
	});

	it("provides the same transport through the React context", () => {
		const runtime = createPluginRuntime({ getAccessToken: () => null });
		const wrapper = ({ children }: { children: ReactNode }) => (
			<PluginRuntimeProvider runtime={runtime}>
				{children}
			</PluginRuntimeProvider>
		);
		const { result } = renderHook(() => usePluginService("example", "api"), {
			wrapper,
		});
		expect(result.current).toBe(runtime.service("example", "api"));
	});
});
