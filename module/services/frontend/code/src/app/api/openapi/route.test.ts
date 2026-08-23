import { readFileSync } from "fs";
import { describe, expect, it, vi } from "vitest";

vi.mock("fs", async (importOriginal) => ({
	...(await importOriginal<typeof import("fs")>()),
	readFileSync: vi.fn(() => '{"openapi":"3.0.0"}'),
}));

import { GET } from "./route";

const mockedReadFileSync = vi.mocked(readFileSync);

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
		mockedReadFileSync.mockImplementationOnce(() => {
			throw new Error("ENOENT");
		});

		const response = GET();
		expect(response.status).toBe(404);
	});
});
