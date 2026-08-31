import { beforeEach, describe, expect, it } from "vitest";
import {
	decodeJWTPayload,
	extractSessionContext,
	resolveSessionUser,
	sessionDisplayLabel,
	storeUserEmail,
	storeUserName,
} from "../auth-session";
import { getToken, setToken } from "../connect/token-store";

const RAW_UUID = "01a0551a-402c-7640-b8a3-ade256505eeb";

function tokenWith(claims: Record<string, unknown>): string {
	const payload = btoa(JSON.stringify(claims));
	return `header.${payload}.sig`;
}

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

describe("Session user identity", () => {
	beforeEach(() => {
		localStorage.clear();
	});

	it("surfaces email and name from the access-token claims", () => {
		const user = resolveSessionUser(
			tokenWith({
				sub: RAW_UUID,
				email: "alice@acme.com",
				name: "Alice Example",
			}),
		);

		expect(user).toEqual({
			id: RAW_UUID,
			email: "alice@acme.com",
			name: "Alice Example",
		});
	});

	it("never renders the raw uuid as the label when a name or email exists", () => {
		const withName = resolveSessionUser(
			tokenWith({
				sub: RAW_UUID,
				email: "alice@acme.com",
				name: "Alice Example",
			}),
		);
		expect(sessionDisplayLabel(withName)).toBe("Alice Example");
		expect(sessionDisplayLabel(withName)).not.toBe(RAW_UUID);

		const emailOnly = resolveSessionUser(
			tokenWith({ sub: RAW_UUID, email: "alice@acme.com" }),
		);
		expect(sessionDisplayLabel(emailOnly)).toBe("alice@acme.com");
		expect(sessionDisplayLabel(emailOnly)).not.toBe(RAW_UUID);
	});

	it("falls back to the uuid only when neither name nor email is known", () => {
		const user = resolveSessionUser(tokenWith({ sub: RAW_UUID }));
		expect(user).toEqual({ id: RAW_UUID, email: undefined, name: undefined });
		expect(sessionDisplayLabel(user)).toBe(RAW_UUID);
	});

	it("recovers name and email from storage after a claim-less refresh token", () => {
		storeUserEmail("alice@acme.com");
		storeUserName("Alice Example");

		// A rotated access token carries only the subject.
		const user = resolveSessionUser(tokenWith({ sub: RAW_UUID }));
		expect(user.email).toBe("alice@acme.com");
		expect(user.name).toBe("Alice Example");
		expect(sessionDisplayLabel(user)).toBe("Alice Example");
	});

	it("prefers an explicit login-response value over the token claim", () => {
		const user = resolveSessionUser(
			tokenWith({ sub: RAW_UUID, email: "stale@acme.com" }),
			{ userId: RAW_UUID, email: "fresh@acme.com" },
		);
		expect(user.email).toBe("fresh@acme.com");
	});
});
