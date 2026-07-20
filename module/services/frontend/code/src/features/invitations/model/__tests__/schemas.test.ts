import { describe, expect, it } from "vitest";
import { createInvitationSchema } from "../schemas";

describe("createInvitationSchema", () => {
	it("accepts valid input with email only (defaults role to member)", () => {
		const result = createInvitationSchema.safeParse({
			email: "alice@example.com",
		});
		expect(result.success).toBe(true);
		if (result.success) {
			expect(result.data.role).toBe("member");
		}
	});

	it("accepts valid input with email and role", () => {
		const result = createInvitationSchema.safeParse({
			email: "bob@example.com",
			role: "admin",
		});
		expect(result.success).toBe(true);
	});

	it("rejects invalid email", () => {
		const result = createInvitationSchema.safeParse({
			email: "not-an-email",
		});
		expect(result.success).toBe(false);
	});

	it("rejects empty email", () => {
		const result = createInvitationSchema.safeParse({ email: "" });
		expect(result.success).toBe(false);
	});

	it("rejects invalid role", () => {
		const result = createInvitationSchema.safeParse({
			email: "a@b.com",
			role: "superadmin",
		});
		expect(result.success).toBe(false);
	});

	it("accepts owner role", () => {
		const result = createInvitationSchema.safeParse({
			email: "a@b.com",
			role: "owner",
		});
		expect(result.success).toBe(true);
	});
});
