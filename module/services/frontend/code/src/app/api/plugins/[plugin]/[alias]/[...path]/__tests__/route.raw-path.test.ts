import { describe, expect, it } from "vitest";

// The sibling route.test.ts mocks the handler to pin the delegation contract.
// This file does the opposite: the REAL route and the REAL handler, because the
// rule under test is about the request URL as it arrives rather than about the
// params, and mocking the handler would delete it.
//
// A catch-all param reaches the handler DECODED, so `records%3Arecover` in the
// URL arrives as the segment `records:recover` — which is exactly the custom
// verb the transport admits and forwards with its colon literal. That is the
// gap: a caller who percent-encoded a colon would have it decoded into a verb
// the binding never declared, while the contract says the colon is literal
// precisely because encoding it names a different resource. The raw path is the
// only place that distinction still exists, so it is screened there.
import { GET, POST } from "@/app/api/plugins/[plugin]/[alias]/[...path]/route";

const TOKEN = `Bearer ${"a".repeat(32)}`;

function context(plugin: string, alias: string, path: string[]) {
	return { params: Promise.resolve({ plugin, alias, path }) };
}

function request(path: string, method = "POST"): Request {
	return new Request(`https://app.example${path}`, {
		method,
		headers: {
			authorization: TOKEN,
			...(method === "POST" ? { "content-type": "application/json" } : {}),
		},
		...(method === "POST" ? { body: "{}" } : {}),
	});
}

async function problemCode(response: Response): Promise<string> {
	return ((await response.json()) as { code: string }).code;
}

describe("the plugin BFF route's raw path", () => {
	it.each([
		["an encoded colon", "records%3Arecover", ["records:recover"]],
		["an encoded separator", "records%2Frecover", ["records/recover"]],
		["an encoded percent", "records%25recover", ["records%recover"]],
		["an encoded backslash", "records%5Crecover", ["records\\recover"]],
		["an encoded NUL", "records%00", ["records\u0000"]],
	])(
		"refuses %s even though the catch-all param arrives decoded",
		async (_label, raw, decoded) => {
			const response = await POST(
				request(`/api/plugins/example/api/${raw}`),
				context("example", "api", decoded),
			);

			expect(response.status).toBe(400);
			expect(await problemCode(response)).toBe("invalid_path");
		},
	);

	it("lets the same literal path through the raw check to resolution", async () => {
		// The control that makes the refusals above mean something: spelled with
		// a literal colon, the identical route passes the path gate and is
		// refused one step later, by resolution — this host installs no plugin
		// service, so the answer is `plugin_service_not_found`. Without this, a
		// route that 400s for any reason at all would look like a passing test.
		const response = await POST(
			request("/api/plugins/example/api/records:recover"),
			context("example", "api", ["records:recover"]),
		);

		expect(response.status).toBe(404);
		expect(await problemCode(response)).toBe("plugin_service_not_found");
	});

	it("screens the raw path before anything is resolved, on a read too", async () => {
		const response = await GET(
			request("/api/plugins/example/api/records%3Arecover", "GET"),
			context("example", "api", ["records:recover"]),
		);

		expect(response.status).toBe(400);
		expect(await problemCode(response)).toBe("invalid_path");
	});
});
