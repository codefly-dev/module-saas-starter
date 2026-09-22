import { describe, expect, it } from "vitest";
import { impersonateUserSchema, suspendUserSchema } from "../schemas";

describe("suspendUserSchema", () => {
	it("accepts valid input", () => {
		const result = suspendUserSchema.safeParse({
			userId: "user-123",
			reason: "Violated terms of service",
		});
		expect(result.success).toBe(true);
	});

	it("rejects empty userId", () => {
		const result = suspendUserSchema.safeParse({
			userId: "",
			reason: "Some reason",
		});
		expect(result.success).toBe(false);
	});

	it("rejects empty reason", () => {
		const result = suspendUserSchema.safeParse({
			userId: "user-123",
			reason: "",
		});
		expect(result.success).toBe(false);
	});

	it("rejects reason longer than 500 characters", () => {
		const result = suspendUserSchema.safeParse({
			userId: "user-123",
			reason: "x".repeat(501),
		});
		expect(result.success).toBe(false);
	});

	it("accepts reason at exactly 500 characters", () => {
		const result = suspendUserSchema.safeParse({
			userId: "user-123",
			reason: "x".repeat(500),
		});
		expect(result.success).toBe(true);
	});

	it("rejects missing fields", () => {
		const result = suspendUserSchema.safeParse({});
		expect(result.success).toBe(false);
	});
});

describe("impersonateUserSchema", () => {
	const parse = (reason: string) =>
		impersonateUserSchema.safeParse({ userId: "user-123", reason });

	it("accepts a justification at exactly the floor", () => {
		expect(parse("abcdefghij").success).toBe(true);
	});

	it("rejects a justification below the floor", () => {
		expect(parse("abcdefghi").success).toBe(false);
	});

	// The server trims and then measures, so padding must not buy length here
	// either — otherwise the dialog accepts a reason the RPC refuses.
	it.each(["         .", "        ok", "   x      ", "          "])(
		"rejects %j, which only clears the floor before trimming",
		(reason) => {
			expect(parse(reason).success).toBe(false);
		},
	);

	it("submits the trimmed value", () => {
		const result = parse("  ticket SUP-4417  ");
		expect(result.success).toBe(true);
		if (result.success) expect(result.data.reason).toBe("ticket SUP-4417");
	});

	// String.length counts UTF-16 units, so five emoji read as ten and would pass
	// a naive .min(10) while the server, counting code points, sees five.
	it("measures code points, not UTF-16 units", () => {
		expect("😀😀😀😀😀".length).toBe(10);
		expect(parse("😀😀😀😀😀").success).toBe(false);
		expect(parse("😀".repeat(10)).success).toBe(true);
	});

	it("rejects beyond 500 code points and accepts exactly 500", () => {
		expect(parse("x".repeat(501)).success).toBe(false);
		expect(parse("x".repeat(500)).success).toBe(true);
		// 300 emoji is 600 UTF-16 units but 300 code points — the server accepts it.
		expect(parse("😀".repeat(300)).success).toBe(true);
	});
});
