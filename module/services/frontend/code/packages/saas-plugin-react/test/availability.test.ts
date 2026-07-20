import { describe, expect, it } from "vitest";

import {
	pluginErrorFromResponse,
	PluginAvailabilityError,
	toPluginFailure,
} from "../src/availability.js";

function problem(
	status: number,
	code: string,
	headers: Record<string, string> = {},
): Response {
	return Response.json(
		{
			code,
			requestId: "body-request",
			title: "private backend detail must not escape",
		},
		{
			status,
			headers: { "content-type": "application/problem+json", ...headers },
		},
	);
}

describe("plugin availability states", () => {
	it.each([
		[503, "backend_unavailable", "unavailable"],
		[404, "plugin_service_not_found", "incompatible"],
		[426, "backend_incompatible", "incompatible"],
		[401, "authentication_required", "failed"],
		[502, "upstream_failed", "failed"],
	] as const)("maps %s/%s to %s", async (status, code, state) => {
		const error = await pluginErrorFromResponse(
			problem(status, code, {
				"retry-after": "30",
				"x-request-id": "header-request",
			}),
		);

		expect(error).toBeInstanceOf(PluginAvailabilityError);
		expect(error.failure).toEqual({
			state,
			code,
			requestId: "header-request",
			retryAfterSeconds: 30,
		});
		expect(error.message).not.toContain("private backend detail");
	});

	it("rejects body-supplied or malformed correlation metadata", async () => {
		const error = await pluginErrorFromResponse(
			problem(500, "unsafe code", {
				"retry-after": "999999999",
				"x-request-id": "unsafe request id with spaces",
			}),
		);
		expect(error.failure).toEqual({
			state: "failed",
			code: "http_500",
		});
		expect(toPluginFailure(new Error("secret render detail"))).toEqual({
			state: "failed",
			code: "render_failed",
		});
	});

	it("refuses to convert a successful response", async () => {
		await expect(pluginErrorFromResponse(new Response("ok"))).rejects.toThrow(
			"requires a failed response",
		);
	});
});
