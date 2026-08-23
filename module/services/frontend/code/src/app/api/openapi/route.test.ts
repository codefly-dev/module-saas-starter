import { describe, expect, it, vi } from "vitest";

const fsMock = vi.hoisted(() => ({
	readFileSync: vi.fn(() => '{"openapi":"3.0.0"}'),
}));

vi.mock("fs", () => fsMock);

import { GET } from "./route";

describe("openapi spec route", () => {
	it("serves the spec without a wildcard CORS header", async () => {
		const response = GET();

		expect(response.status).toBe(200);
		expect(response.headers.get("access-control-allow-origin")).toBeNull();
		expect(response.headers.get("content-type")).toContain(
			"application/json",
		);
		expect(await response.text()).toContain("openapi");
	});

	it("returns 404 when the spec is missing", () => {
		fsMock.readFileSync.mockImplementationOnce(() => {
			throw new Error("ENOENT");
		});

		const response = GET();
		expect(response.status).toBe(404);
	});
});
