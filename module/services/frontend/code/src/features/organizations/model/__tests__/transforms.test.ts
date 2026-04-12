import { describe, it, expect } from "vitest";
import { getRoleBadgeVariant, roleLabel, slugify } from "../transforms";

describe("getRoleBadgeVariant", () => {
  it("returns 'default' for owner", () => {
    expect(getRoleBadgeVariant("owner")).toBe("default");
  });

  it("returns 'secondary' for admin", () => {
    expect(getRoleBadgeVariant("admin")).toBe("secondary");
  });

  it("returns 'outline' for member", () => {
    expect(getRoleBadgeVariant("member")).toBe("outline");
  });

  it("returns 'outline' for unspecified", () => {
    expect(getRoleBadgeVariant("unspecified")).toBe("outline");
  });
});

describe("roleLabel", () => {
  it("returns 'Owner' for owner", () => {
    expect(roleLabel("owner")).toBe("Owner");
  });

  it("returns 'Admin' for admin", () => {
    expect(roleLabel("admin")).toBe("Admin");
  });

  it("returns 'Member' for member", () => {
    expect(roleLabel("member")).toBe("Member");
  });

  it("returns 'Unknown' for unspecified", () => {
    expect(roleLabel("unspecified")).toBe("Unknown");
  });
});

describe("slugify", () => {
  it("converts name to lowercase kebab-case", () => {
    expect(slugify("Acme Corp")).toBe("acme-corp");
  });

  it("removes special characters", () => {
    expect(slugify("Hello, World!")).toBe("hello-world");
  });

  it("trims leading/trailing hyphens", () => {
    expect(slugify("  --hello--  ")).toBe("hello");
  });

  it("collapses multiple non-alphanumeric runs into single hyphen", () => {
    expect(slugify("foo   bar___baz")).toBe("foo-bar-baz");
  });

  it("handles empty string", () => {
    expect(slugify("")).toBe("");
  });

  it("handles already-valid slug", () => {
    expect(slugify("my-org-123")).toBe("my-org-123");
  });
});
