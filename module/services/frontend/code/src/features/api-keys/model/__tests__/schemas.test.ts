import { describe, it, expect } from "vitest";
import { createKeySchema } from "../schemas";

describe("createKeySchema", () => {
  it("accepts valid input with name only (defaults environment to 1)", () => {
    const result = createKeySchema.safeParse({ name: "My API Key" });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.environment).toBe(1);
    }
  });

  it("accepts valid input with name and environment", () => {
    const result = createKeySchema.safeParse({
      name: "Test Key",
      environment: 2,
    });
    expect(result.success).toBe(true);
  });

  it("rejects empty name", () => {
    const result = createKeySchema.safeParse({ name: "" });
    expect(result.success).toBe(false);
  });

  it("rejects missing name", () => {
    const result = createKeySchema.safeParse({});
    expect(result.success).toBe(false);
  });

  it("accepts name with special characters", () => {
    const result = createKeySchema.safeParse({
      name: "My Key - Production (v2)",
    });
    expect(result.success).toBe(true);
  });
});
