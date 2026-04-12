import { describe, it, expect } from "vitest";
import { formatDate, formatLimit, cn } from "../utils";

describe("formatDate", () => {
  it("formats a valid ISO date", () => {
    const result = formatDate("2026-03-28T12:00:00Z");
    expect(result).toContain("2026");
    expect(result).toContain("Mar");
  });

  it("returns dash for empty string", () => {
    expect(formatDate("")).toBe("-");
  });

  it("returns dash for undefined-like input", () => {
    expect(formatDate(undefined as unknown as string)).toBe("-");
  });
});

describe("formatLimit", () => {
  it("returns Unlimited for -1", () => {
    expect(formatLimit(-1)).toBe("Unlimited");
  });

  it("returns Disabled for 0", () => {
    expect(formatLimit(0)).toBe("Disabled");
  });

  it("formats positive numbers with locale", () => {
    expect(formatLimit(5)).toBe("5");
    expect(formatLimit(10000)).toContain("10");
  });
});

describe("cn", () => {
  it("merges class names", () => {
    expect(cn("px-2", "py-1")).toBe("px-2 py-1");
  });

  it("handles conditional classes", () => {
    expect(cn("base", false && "hidden", "extra")).toBe("base extra");
  });

  it("resolves tailwind conflicts", () => {
    expect(cn("px-2", "px-4")).toBe("px-4");
  });
});
