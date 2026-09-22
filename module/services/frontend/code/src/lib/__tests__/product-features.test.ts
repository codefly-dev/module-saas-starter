import { describe, expect, it } from "vitest";
import { isProductRouteEnabled, productFeatures } from "../product-features";

describe("optional product features", () => {
	it("defaults subscriptions and SSO off", () => {
		expect(productFeatures).toEqual({ subscriptions: false, sso: false });
	});
	it("gates only the selected optional surfaces, including nested routes", () => {
		const disabled = { subscriptions: false, sso: false };
		for (const path of [
			"/admin/billing",
			"/admin/billing/success",
			"/admin/sso",
		])
			expect(isProductRouteEnabled(path, disabled)).toBe(false);
		expect(isProductRouteEnabled("/admin/entitlements", disabled)).toBe(true);
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
