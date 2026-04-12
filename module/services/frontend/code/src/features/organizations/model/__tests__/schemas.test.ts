import { describe, it, expect } from "vitest";
import { createOrgSchema, addOrgMemberSchema } from "../schemas";

describe("createOrgSchema", () => {
  it("accepts valid input", () => {
    const result = createOrgSchema.safeParse({
      name: "Acme Corp",
      slug: "acme-corp",
    });
    expect(result.success).toBe(true);
  });

  it("rejects empty name", () => {
    const result = createOrgSchema.safeParse({ name: "", slug: "acme" });
    expect(result.success).toBe(false);
  });

  it("rejects empty slug", () => {
    const result = createOrgSchema.safeParse({ name: "Acme", slug: "" });
    expect(result.success).toBe(false);
  });

  it("rejects slug with uppercase letters", () => {
    const result = createOrgSchema.safeParse({ name: "Acme", slug: "Acme-Corp" });
    expect(result.success).toBe(false);
  });

  it("rejects slug with spaces", () => {
    const result = createOrgSchema.safeParse({ name: "Acme", slug: "acme corp" });
    expect(result.success).toBe(false);
  });

  it("accepts slug with hyphens and numbers", () => {
    const result = createOrgSchema.safeParse({
      name: "Acme 2",
      slug: "acme-2",
    });
    expect(result.success).toBe(true);
  });

  it("rejects name longer than 100 characters", () => {
    const result = createOrgSchema.safeParse({
      name: "x".repeat(101),
      slug: "valid",
    });
    expect(result.success).toBe(false);
  });
});

describe("addOrgMemberSchema", () => {
  it("accepts valid member role", () => {
    const result = addOrgMemberSchema.safeParse({
      userId: "user-1",
      role: "member",
    });
    expect(result.success).toBe(true);
  });

  it("rejects invalid role", () => {
    const result = addOrgMemberSchema.safeParse({
      userId: "user-1",
      role: "superadmin",
    });
    expect(result.success).toBe(false);
  });

  it("rejects empty userId", () => {
    const result = addOrgMemberSchema.safeParse({
      userId: "",
      role: "admin",
    });
    expect(result.success).toBe(false);
  });
});
