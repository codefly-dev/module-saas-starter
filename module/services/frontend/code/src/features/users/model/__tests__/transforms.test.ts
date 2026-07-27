import { describe, expect, it } from "vitest";
import {
	getStatusBadgeVariant,
	sortByCreated,
	statusLabel,
	toDisplayName,
	withDisplayName,
} from "../transforms";

describe("getStatusBadgeVariant", () => {
	it("returns 'default' for active", () => {
		expect(getStatusBadgeVariant("active")).toBe("default");
	});

	it("returns 'secondary' for inactive", () => {
		expect(getStatusBadgeVariant("inactive")).toBe("secondary");
	});

	it("returns 'destructive' for suspended", () => {
		expect(getStatusBadgeVariant("suspended")).toBe("destructive");
	});

	it("returns 'outline' for deleted", () => {
		expect(getStatusBadgeVariant("deleted")).toBe("outline");
	});

	it("returns 'secondary' for unspecified", () => {
		expect(getStatusBadgeVariant("unspecified")).toBe("secondary");
	});
});

describe("statusLabel", () => {
	it("returns 'Active' for active", () => {
		expect(statusLabel("active")).toBe("Active");
	});

	it("returns 'Suspended' for suspended", () => {
		expect(statusLabel("suspended")).toBe("Suspended");
	});

	it("returns 'Unknown' for unspecified", () => {
		expect(statusLabel("unspecified")).toBe("Unknown");
	});
});

describe("toDisplayName", () => {
	it("prefers the canonical profile name", () => {
		expect(
			toDisplayName(
				{
					name: "Antoine Toussaint",
					first_name: "Stale",
					last_name: "Value",
				},
				"antoine@example.com",
			),
		).toBe("Antoine Toussaint");
	});

	it("returns full name when first and last are present", () => {
		expect(
			toDisplayName(
				{ first_name: "Alice", last_name: "Smith" },
				"alice@corp.com",
			),
		).toBe("Alice Smith");
	});

	it("returns first name only when last name is missing", () => {
		expect(toDisplayName({ first_name: "Alice" }, "alice@corp.com")).toBe(
			"Alice",
		);
	});

	it("falls back to email when profile has no names", () => {
		expect(toDisplayName({}, "alice@corp.com")).toBe("alice@corp.com");
	});

	it("falls back to email when names are empty strings", () => {
		expect(
			toDisplayName({ first_name: "", last_name: "" }, "bob@test.com"),
		).toBe("bob@test.com");
	});
});

describe("withDisplayName", () => {
	it("trims the name and preserves unrelated profile attributes", () => {
		expect(
			withDisplayName(
				{ locale: "fr", first_name: "Antoine" },
				"  Antoine Toussaint  ",
			),
		).toEqual({
			locale: "fr",
			first_name: "Antoine",
			name: "Antoine Toussaint",
		});
	});
});

describe("sortByCreated", () => {
	it("sorts items by createdAt descending (newest first)", () => {
		const items = [
			{ createdAt: "2024-01-01T00:00:00Z", id: "old" },
			{ createdAt: "2024-06-01T00:00:00Z", id: "new" },
			{ createdAt: "2024-03-01T00:00:00Z", id: "mid" },
		];
		const sorted = sortByCreated(items);
		expect(sorted.map((i) => i.id)).toEqual(["new", "mid", "old"]);
	});

	it("pushes items with undefined createdAt to the end", () => {
		const items = [
			{ createdAt: undefined, id: "no-date" },
			{ createdAt: "2024-01-01T00:00:00Z", id: "dated" },
		];
		const sorted = sortByCreated(items);
		expect(sorted[0].id).toBe("dated");
		expect(sorted[1].id).toBe("no-date");
	});

	it("returns empty array for empty input", () => {
		expect(sortByCreated([])).toEqual([]);
	});

	it("does not mutate the original array", () => {
		const items = [
			{ createdAt: "2024-06-01T00:00:00Z" },
			{ createdAt: "2024-01-01T00:00:00Z" },
		];
		const original = [...items];
		sortByCreated(items);
		expect(items).toEqual(original);
	});
});
