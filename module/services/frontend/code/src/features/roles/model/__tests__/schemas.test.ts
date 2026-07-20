import { describe, expect, it } from "vitest";
import { assignRoleSchema, createRoleSchema } from "../schemas";

describe("createRoleSchema", () => {
	it("accepts valid input with name only", () => {
		const result = createRoleSchema.safeParse({ name: "Editor" });
		expect(result.success).toBe(true);
	});

	it("accepts valid input with all fields", () => {
		const result = createRoleSchema.safeParse({
			name: "Editor",
			description: "Can edit content",
			permissions: [{ resource: "posts", action: "write" }],
		});
		expect(result.success).toBe(true);
	});

	it("rejects empty name", () => {
		const result = createRoleSchema.safeParse({ name: "" });
		expect(result.success).toBe(false);
	});

	it("defaults permissions to empty array", () => {
		const result = createRoleSchema.safeParse({ name: "Viewer" });
		expect(result.success).toBe(true);
		if (result.success) {
			expect(result.data.permissions).toEqual([]);
		}
	});

	it("rejects permissions with empty resource", () => {
		const result = createRoleSchema.safeParse({
			name: "Bad",
			permissions: [{ resource: "", action: "read" }],
		});
		expect(result.success).toBe(false);
	});

	it("rejects permissions with empty action", () => {
		const result = createRoleSchema.safeParse({
			name: "Bad",
			permissions: [{ resource: "posts", action: "" }],
		});
		expect(result.success).toBe(false);
	});
});

describe("assignRoleSchema", () => {
	it("accepts valid input", () => {
		const result = assignRoleSchema.safeParse({
			subjectId: "user-1",
			roleId: "role-1",
		});
		expect(result.success).toBe(true);
	});

	it("accepts optional orgId", () => {
		const result = assignRoleSchema.safeParse({
			subjectId: "user-1",
			roleId: "role-1",
			orgId: "org-1",
		});
		expect(result.success).toBe(true);
	});

	it("rejects empty subjectId", () => {
		const result = assignRoleSchema.safeParse({
			subjectId: "",
			roleId: "role-1",
		});
		expect(result.success).toBe(false);
	});

	it("rejects empty roleId", () => {
		const result = assignRoleSchema.safeParse({
			subjectId: "user-1",
			roleId: "",
		});
		expect(result.success).toBe(false);
	});
});
