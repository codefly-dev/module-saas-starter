import { describe, expect, it } from "vitest";
import { developmentOrigins } from "../../server/development-origins.mjs";

describe("developmentOrigins", () => {
  it("adds no origins by default", () => {
    expect(developmentOrigins()).toEqual([]);
    expect(developmentOrigins("  ")).toEqual([]);
  });
  it("normalizes explicit origins and deduplicates hostnames", () => {
    expect(developmentOrigins("https://App.Example.test:8443,app.example.test http://127.0.0.1:3000")).toEqual(["app.example.test", "127.0.0.1"]);
  });
  it.each(["*", "*.example.test", "https://user:password@example.test", "https://example.test/path", "https://example.test?x=1", "https://example.test#x", "ftp://example.test", "https://"])("rejects ambiguous or overbroad configuration: %s", (value) => {
    expect(() => developmentOrigins(value)).toThrow("FRONTEND_ALLOWED_DEV_ORIGINS");
  });
});
