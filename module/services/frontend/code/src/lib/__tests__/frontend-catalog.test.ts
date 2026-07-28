import type { Transport } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import {
	ACCOUNT_SERVICE_DESCRIPTORS,
	API_KEY_SCOPES,
	createAccountsClients,
	ENTITLEMENTS,
	isEntitlement,
	isPermission,
	PERMISSIONS,
} from "@/gen/saas/accounts/v1/frontend_catalog";

describe("generated frontend catalog", () => {
	it("covers every accounts service and procedure", () => {
		expect(Object.keys(ACCOUNT_SERVICE_DESCRIPTORS)).toHaveLength(26);
		const procedureCount = Object.values(ACCOUNT_SERVICE_DESCRIPTORS).reduce(
			(count, service) => count + Object.keys(service.method).length,
			0,
		);
		expect(procedureCount).toBe(135);
		expect(
			ACCOUNT_SERVICE_DESCRIPTORS.WorkContextService.method.exchangeAudience,
		).toBeDefined();
	});

	it("creates one typed client for every service", () => {
		const transport = {} as Transport;
		expect(Object.keys(createAccountsClients(transport))).toEqual(
			Object.keys(ACCOUNT_SERVICE_DESCRIPTORS),
		);
	});

	it("publishes the canonical permission and scope vocabulary", () => {
		expect(Object.values(PERMISSIONS)).toHaveLength(21);
		expect(API_KEY_SCOPES).toHaveLength(19);
		expect(isPermission(PERMISSIONS.ROLES_WRITE)).toBe(true);
		expect(isPermission("roles:delete")).toBe(false);
	});

	it("publishes all server-owned entitlement keys", () => {
		expect(Object.values(ENTITLEMENTS)).toEqual([
			"api_calls_monthly",
			"api_keys",
			"audit_log",
			"seats",
			"sso",
		]);
		expect(isEntitlement(ENTITLEMENTS.SEATS)).toBe(true);
		expect(isEntitlement("teams")).toBe(false);
	});
});
