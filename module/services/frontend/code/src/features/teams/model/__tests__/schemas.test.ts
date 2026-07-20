import { describe, expect, it } from "vitest";
import { addTeamMemberSchema, createTeamSchema } from "../schemas";

describe("createTeamSchema", () => {
	it("accepts valid input with name only", () => {
		const result = createTeamSchema.safeParse({ name: "Engineering" });
		expect(result.success).toBe(true);
	});

	it("accepts valid input with name and description", () => {
		const result = createTeamSchema.safeParse({
			name: "Engineering",
			description: "The engineering team",
		});
		expect(result.success).toBe(true);
	});

	it("rejects empty name", () => {
		const result = createTeamSchema.safeParse({ name: "" });
		expect(result.success).toBe(false);
	});

	it("rejects name longer than 100 characters", () => {
		const result = createTeamSchema.safeParse({ name: "x".repeat(101) });
		expect(result.success).toBe(false);
	});

	it("rejects description longer than 500 characters", () => {
		const result = createTeamSchema.safeParse({
			name: "Team",
			description: "x".repeat(501),
		});
		expect(result.success).toBe(false);
	});

	it("defaults description to empty string when omitted", () => {
		const result = createTeamSchema.safeParse({ name: "Team" });
		expect(result.success).toBe(true);
		if (result.success) {
			expect(result.data.description).toBe("");
		}
	});
});

describe("addTeamMemberSchema", () => {
	it("accepts valid member role", () => {
		const result = addTeamMemberSchema.safeParse({
			userId: "user-1",
			role: "member",
		});
		expect(result.success).toBe(true);
	});

	it("accepts admin role", () => {
		const result = addTeamMemberSchema.safeParse({
			userId: "user-1",
			role: "admin",
		});
		expect(result.success).toBe(true);
	});

	it("rejects owner role (not in enum)", () => {
		const result = addTeamMemberSchema.safeParse({
			userId: "user-1",
			role: "owner",
		});
		expect(result.success).toBe(false);
	});

	it("rejects empty userId", () => {
		const result = addTeamMemberSchema.safeParse({
			userId: "",
			role: "member",
		});
		expect(result.success).toBe(false);
	});
});
