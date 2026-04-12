import { describe, it, expect } from "vitest";
import { suspendUserSchema } from "../schemas";

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
