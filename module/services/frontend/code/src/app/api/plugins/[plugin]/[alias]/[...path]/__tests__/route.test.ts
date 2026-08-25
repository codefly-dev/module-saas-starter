import { afterEach, describe, expect, it, vi } from "vitest";

// The handler's behaviour — auth, same-origin, path safety, header allowlist,
// proxying — is covered exhaustively by src/lib/__tests__/plugin-bff.test.ts.
// This route file is a thin wrapper whose ONLY job is to await context.params
// and forward the resolved values to handlePluginBffRequest, so we mock the
// handler and assert exactly that delegation contract in isolation.
const { handlePluginBffRequest } = vi.hoisted(() => ({
	// Typed via the generic so `.mock.calls` carries (request, params); the impl
	// itself ignores them and returns a sentinel response.
	handlePluginBffRequest: vi.fn<
		(
			request: Request,
			params: { plugin: string; alias: string; path: string[] },
		) => Promise<Response>
	>(async () => new Response("delegated", { status: 207 })),
}));
vi.mock("../../../../../../../../server/plugin-bff", () => ({
	handlePluginBffRequest,
}));

import {
	DELETE,
	GET,
	HEAD,
	OPTIONS,
	PATCH,
	POST,
	PUT,
} from "@/app/api/plugins/[plugin]/[alias]/[...path]/route";

function context(plugin: string, alias: string, path: string[]) {
	return { params: Promise.resolve({ plugin, alias, path }) };
}

afterEach(() => handlePluginBffRequest.mockClear());

describe("plugin BFF route wrapper", () => {
	it("awaits the route params and forwards request + params verbatim", async () => {
		const request = new Request(
			"http://localhost:3000/api/plugins/billing/rest/invoices/42",
		);

		const res = await GET(
			request,
			context("billing", "rest", ["invoices", "42"]),
		);

		expect(handlePluginBffRequest).toHaveBeenCalledTimes(1);
		const [passedRequest, passedParams] = handlePluginBffRequest.mock.calls[0];
		expect(passedRequest).toBe(request);
		// toEqual against a plain object proves the params Promise was awaited —
		// an un-awaited Promise would never match this shape.
		expect(passedParams).toEqual({
			plugin: "billing",
			alias: "rest",
			path: ["invoices", "42"],
		});
		// The handler's Response is returned unchanged.
		expect(res.status).toBe(207);
	});

	it("routes every exported HTTP method through the handler", async () => {
		const methods = { GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS };

		for (const method of Object.values(methods)) {
			await method(
				new Request("http://localhost:3000/api/plugins/p/a/x"),
				context("p", "a", ["x"]),
			);
		}

		expect(handlePluginBffRequest).toHaveBeenCalledTimes(
			Object.keys(methods).length,
		);
	});
});
