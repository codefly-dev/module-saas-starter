import { describe, expect, it } from "vitest";
import { isProductRouteEnabled, productFeatures } from "../product-features";

describe("optional product features", () => {
	it("defaults subscriptions, SSO, and entitlements off", () => {
		expect(productFeatures).toEqual({
			subscriptions: false,
			sso: false,
			entitlements: false,
		});
	});
	it("gates only the selected optional surfaces, including nested routes", () => {
		const disabled = { subscriptions: false, sso: false, entitlements: false };
		for (const path of [
			"/admin/billing",
			"/admin/billing/success",
			"/admin/sso",
			"/admin/entitlements",
			"/admin/entitlements/example",
		])
			expect(isProductRouteEnabled(path, disabled)).toBe(false);
		expect(
			isProductRouteEnabled("/admin/entitlements", {
				...disabled,
				entitlements: true,
			}),
		).toBe(true);
		expect(isProductRouteEnabled("/admin/teams", disabled)).toBe(true);
		expect(
			isProductRouteEnabled("/admin/billing", {
				...disabled,
				subscriptions: true,
			}),
		).toBe(true);
		expect(
			isProductRouteEnabled("/admin/sso", { ...disabled, sso: true }),
		).toBe(true);
	});
});
