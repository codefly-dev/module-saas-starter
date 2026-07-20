import { describe, expect, it } from "vitest";
import { toExampleViewRows } from "./transforms";

describe("toExampleViewRows", () => {
	it("sorts by createdAt ascending", () => {
		const rows = toExampleViewRows([
			{ id: "2", name: "b", createdAt: "2026-02-01T00:00:00Z" },
			{ id: "1", name: "a", createdAt: "2026-01-01T00:00:00Z" },
		]);
		expect(rows.map((r) => r.id)).toEqual(["1", "2"]);
	});

	it("formats display with truncated date", () => {
		const rows = toExampleViewRows([
			{ id: "1", name: "foo", createdAt: "2026-04-11T12:34:56Z" },
		]);
		expect(rows[0].display).toBe("foo (2026-04-11)");
	});

	it("is a pure function (does not mutate input)", () => {
		const input = [
			{ id: "b", name: "b", createdAt: "2026-02-01" },
			{ id: "a", name: "a", createdAt: "2026-01-01" },
		];
		const snapshot = JSON.stringify(input);
		toExampleViewRows(input);
		expect(JSON.stringify(input)).toBe(snapshot);
	});

	it("returns empty for empty input", () => {
		expect(toExampleViewRows([])).toEqual([]);
	});
});
