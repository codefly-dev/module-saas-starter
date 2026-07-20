import { beforeEach, describe, expect, it } from "vitest";
import { decodeJWTPayload, extractSessionContext } from "../auth-session";
import { getToken, setToken } from "../connect/token-store";

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

describe("JWT payload decoding", () => {
	it("decodes a valid JWT payload", () => {
		// Header: {"alg":"EdDSA"}, Payload: {"sub":"user-123","iss":"saas-starter"}
		const header = btoa(JSON.stringify({ alg: "EdDSA" }));
		const payload = btoa(
			JSON.stringify({ sub: "user-123", iss: "saas-starter", org: "org-456" }),
		);
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
		const payload = btoa(
			JSON.stringify({
				sub: "user-1",
				org: "org-1",
				or: "admin",
				pr: "super_admin",
			}),
		);
		const token = `x.${payload}.x`;
		const decoded = decodeJWTPayload(token);

		expect(decoded.or).toBe("admin");
		expect(decoded.pr).toBe("super_admin");
	});

	it("extracts the signed organization selection with its roles", () => {
		const payload = btoa(
			JSON.stringify({
				sub: "user-1",
				org: "org-2",
				or: "member",
				pr: "support",
			}),
		);
		const context = extractSessionContext(`x.${payload}.x`);

		expect(context).toEqual({
			organizationId: "org-2",
			orgRole: "member",
			platformRole: "support",
		});
	});

	it("accepts only canonical compact role claims", () => {
		const payload = btoa(
			JSON.stringify({
				org: "org-2",
				org_role: "owner",
				platform_role: "super_admin",
				or: "root",
				pr: "administrator",
			}),
		);

		expect(extractSessionContext(`x.${payload}.x`)).toEqual({
			organizationId: "org-2",
			orgRole: undefined,
			platformRole: undefined,
		});
	});
});
