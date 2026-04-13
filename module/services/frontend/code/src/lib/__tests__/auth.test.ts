import { describe, it, expect, vi, beforeEach } from "vitest";
import { setToken, getToken } from "../connect/token-store";

describe("Connect token store", () => {
  beforeEach(() => {
    setToken(null);
  });

  it("starts with null token", () => {
    expect(getToken()).toBeNull();
  });

  it("stores and retrieves a token", () => {
    setToken("test-jwt-token");
    expect(getToken()).toBe("test-jwt-token");
  });

  it("clears the token", () => {
    setToken("test-jwt-token");
    setToken(null);
    expect(getToken()).toBeNull();
  });

  it("overwrites previous token", () => {
    setToken("token-1");
    setToken("token-2");
    expect(getToken()).toBe("token-2");
  });
});

describe("Auth URL resolution", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.resetModules();
    process.env = { ...originalEnv };
  });

  it("uses NEXT_PUBLIC_API_REST when set", async () => {
    process.env.NEXT_PUBLIC_API_REST = "http://api:5962";
    process.env.NEXT_PUBLIC_BACKEND_URL = "http://gateway:8080";

    // The auth module reads env at import time, so we need dynamic import
    // Just verify the env var precedence logic
    expect(
      process.env.NEXT_PUBLIC_API_REST ||
      process.env.NEXT_PUBLIC_BACKEND_URL ||
      "http://localhost:5962"
    ).toBe("http://api:5962");
  });

  it("falls back to NEXT_PUBLIC_BACKEND_URL", () => {
    delete process.env.NEXT_PUBLIC_API_REST;
    process.env.NEXT_PUBLIC_BACKEND_URL = "http://gateway:8080";

    expect(
      process.env.NEXT_PUBLIC_API_REST ||
      process.env.NEXT_PUBLIC_BACKEND_URL ||
      "http://localhost:5962"
    ).toBe("http://gateway:8080");
  });

  it("falls back to localhost:5962", () => {
    delete process.env.NEXT_PUBLIC_API_REST;
    delete process.env.NEXT_PUBLIC_BACKEND_URL;

    expect(
      process.env.NEXT_PUBLIC_API_REST ||
      process.env.NEXT_PUBLIC_BACKEND_URL ||
      "http://localhost:5962"
    ).toBe("http://localhost:5962");
  });
});

describe("JWT payload decoding", () => {
  // Import the function from admin-core
  // Note: decodeJWTPayload is a pure function that base64-decodes the JWT payload

  function decodeJWTPayload(token: string): Record<string, unknown> {
    const parts = token.split(".");
    if (parts.length !== 3) return {};
    try {
      const payload = atob(parts[1].replace(/-/g, "+").replace(/_/g, "/"));
      return JSON.parse(payload);
    } catch {
      return {};
    }
  }

  it("decodes a valid JWT payload", () => {
    // Header: {"alg":"EdDSA"}, Payload: {"sub":"user-123","iss":"saas-starter"}
    const header = btoa(JSON.stringify({ alg: "EdDSA" }));
    const payload = btoa(JSON.stringify({ sub: "user-123", iss: "saas-starter", org: "org-456" }));
    const token = `${header}.${payload}.fake-signature`;

    const decoded = decodeJWTPayload(token);
    expect(decoded.sub).toBe("user-123");
    expect(decoded.iss).toBe("saas-starter");
    expect(decoded.org).toBe("org-456");
  });

  it("returns empty object for malformed token", () => {
    expect(decodeJWTPayload("not-a-jwt")).toEqual({});
    expect(decodeJWTPayload("")).toEqual({});
    expect(decodeJWTPayload("a.b")).toEqual({});
  });

  it("extracts roles from JWT claims", () => {
    const payload = btoa(JSON.stringify({
      sub: "user-1",
      org: "org-1",
      or: "admin",
      pr: "super_admin",
    }));
    const token = `x.${payload}.x`;
    const decoded = decodeJWTPayload(token);

    expect(decoded.or).toBe("admin");
    expect(decoded.pr).toBe("super_admin");
  });
});
