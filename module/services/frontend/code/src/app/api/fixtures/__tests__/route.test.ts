import { afterEach, describe, expect, it, vi } from "vitest";

const { activeFixtureName, loadActiveFixture } = vi.hoisted(() => ({
	activeFixtureName: vi.fn<() => string | null>(),
	loadActiveFixture: vi.fn<() => unknown>(),
}));
vi.mock("@/lib/fixtures/loader", () => ({
	activeFixtureName,
	loadActiveFixture,
}));

import { GET } from "@/app/api/fixtures/route";

afterEach(() => {
	activeFixtureName.mockReset();
	loadActiveFixture.mockReset();
});

describe("fixtures route", () => {
	it("404s when no fixture is active (the dev-auth boundary is closed)", async () => {
		activeFixtureName.mockReturnValue(null);

		const res = GET();

		expect(res.status).toBe(404);
		await expect(res.json()).resolves.toEqual({ error: "no active fixture" });
		expect(loadActiveFixture).not.toHaveBeenCalled();
	});

	it("404s when the active fixture name resolves to no file", async () => {
		activeFixtureName.mockReturnValue("dev-admin");
		loadActiveFixture.mockReturnValue(null);

		const res = GET();

		expect(res.status).toBe(404);
		await expect(res.json()).resolves.toEqual({
			error: 'fixture "dev-admin" not found',
		});
	});

	it("returns the active fixture data tagged with its name", async () => {
		activeFixtureName.mockReturnValue("dev-admin");
		loadActiveFixture.mockReturnValue({ users: [{ email: "a@test.com" }] });

		const res = GET();

		expect(res.status).toBe(200);
		await expect(res.json()).resolves.toEqual({
			name: "dev-admin",
			users: [{ email: "a@test.com" }],
		});
	});
});
