import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/lib/connect/token-store";
import { hostPluginRuntime } from "@/lib/plugins/runtime";

afterEach(() => {
	setToken(null);
	vi.restoreAllMocks();
});

describe("host plugin runtime adapter", () => {
	it("reads the private token store lazily without exposing it to the product", async () => {
		const fetchMock = vi
			.spyOn(globalThis, "fetch")
			.mockResolvedValue(new Response("{}"));
		const service = hostPluginRuntime.service("example", "api");

		setToken("host-token");
		await service.request("traffic");
		setToken("refreshed-token");
		await service.request("traffic");

		expect(Object.keys(hostPluginRuntime)).toEqual(["service"]);
		expect(
			new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization"),
		).toBe("Bearer host-token");
		expect(
			new Headers(fetchMock.mock.calls[1]?.[1]?.headers).get("authorization"),
		).toBe("Bearer refreshed-token");
	});
});
