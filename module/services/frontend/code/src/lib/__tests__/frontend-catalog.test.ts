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
		// No raw service/procedure totals here — they churn on every endpoint
		// and the generated catalog is already pinned byte-for-byte by the Go
		// cataloggen fixture-equality (frontend_catalog.ts). Assert structure:
		// representative procedures resolve, and every service gets a client
		// (the keys-match invariant in the next test).
		expect(
			ACCOUNT_SERVICE_DESCRIPTORS.WorkContextService.method.exchangeAudience,
		).toBeDefined();
		expect(
			ACCOUNT_SERVICE_DESCRIPTORS.UsageService.method.getUsageHistory,
		).toBeDefined();
	});

	it("creates one typed client for every service", () => {
		const transport = {} as Transport;
		expect(Object.keys(createAccountsClients(transport))).toEqual(
			Object.keys(ACCOUNT_SERVICE_DESCRIPTORS),
		);
	});

	it("publishes the canonical permission and scope vocabulary", () => {
		expect(Object.values(PERMISSIONS)).toHaveLength(24);
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
