import { describe, it, expect } from "vitest";
import { getRoleBadgeVariant, roleLabel } from "../transforms";

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
