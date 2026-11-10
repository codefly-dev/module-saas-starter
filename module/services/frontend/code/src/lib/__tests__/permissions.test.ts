import { describe, expect, it } from "vitest";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { hasPermission } from "../permissions";

describe("a platform role is not an organization permission", () => {
	it("gives super_admin none of an organization's permissions", () => {
		for (const permission of [
			PERMISSIONS.ROLES_WRITE,
			PERMISSIONS.AUDIT_READ,
			PERMISSIONS.USERS_READ,
		]) {
			expect(hasPermission("super_admin", undefined, permission)).toBe(false);
		}
	});

	it("keeps the platform's own surface for super_admin", () => {
		expect(
			hasPermission(
				"super_admin",
				undefined,
				PERMISSIONS.MODULE_ENVELOPE_WRITE,
			),
		).toBe(true);
	});

	it("gives support and billing nothing without membership", () => {
		expect(hasPermission("support", undefined, PERMISSIONS.AUDIT_READ)).toBe(
			false,
		);
		expect(
			hasPermission("billing", undefined, PERMISSIONS.ENTITLEMENTS_READ),
		).toBe(false);
	});

	it("still follows the organization role", () => {
		expect(hasPermission(undefined, "admin", PERMISSIONS.ROLES_WRITE)).toBe(
			true,
		);
		expect(
			hasPermission("super_admin", "member", PERMISSIONS.ROLES_WRITE),
		).toBe(false);
	});
});
