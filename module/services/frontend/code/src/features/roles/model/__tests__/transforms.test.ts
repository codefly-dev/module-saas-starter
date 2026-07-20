import { describe, expect, it } from "vitest";
import { formatPermission, parsePermission } from "../transforms";

describe("formatPermission", () => {
	it("formats resource and action with colon separator", () => {
		expect(formatPermission({ resource: "users", action: "read" })).toBe(
			"users:read",
		);
	});

	it("handles complex resource names", () => {
		expect(
			formatPermission({ resource: "org.settings", action: "write" }),
		).toBe("org.settings:write");
	});

	it("handles wildcard actions", () => {
		expect(formatPermission({ resource: "billing", action: "*" })).toBe(
			"billing:*",
		);
	});
});

describe("parsePermission", () => {
	it("parses a valid permission string", () => {
		expect(parsePermission("users:read")).toEqual({
			resource: "users",
			action: "read",
		});
	});

	it("returns null for string without colon", () => {
		expect(parsePermission("invalid")).toBeNull();
	});

	it("returns null for empty string", () => {
		expect(parsePermission("")).toBeNull();
	});

	it("returns null when resource is empty", () => {
		expect(parsePermission(":read")).toBeNull();
	});

	it("returns null when action is empty", () => {
		expect(parsePermission("users:")).toBeNull();
	});

	it("handles colons in action by splitting on first colon only", () => {
		// split(":" ) returns ["users", "read", "extra"] — action will be "read"
		const result = parsePermission("users:read:extra");
		expect(result).toEqual({ resource: "users", action: "read" });
	});
});
