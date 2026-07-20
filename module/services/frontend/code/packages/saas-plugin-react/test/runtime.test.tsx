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
	])("rejects an unsafe product path '%s'", async (path) => {
		const service = createPluginRuntime({ getAccessToken: () => null }).service(
			"example",
			"api",
		);
		await expect(service.request(path)).rejects.toMatchObject({
			code: "invalid_path",
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
