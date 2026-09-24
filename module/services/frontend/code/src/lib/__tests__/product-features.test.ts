import { describe, expect, it } from "vitest";
import { isProductRouteEnabled } from "../product-features";
import {
	resolvePublicRuntimeConfig,
	type WorkspaceReader,
} from "../public-runtime-config";

const reader =
	(groups: Record<string, Record<string, string>>): WorkspaceReader =>
	(group, key) =>
		groups[group]?.[key];

describe("optional product features", () => {
	it("defaults subscriptions, SSO, and entitlements off", () => {
		expect(resolvePublicRuntimeConfig(reader({})).productFeatures).toEqual({
			subscriptions: false,
			sso: false,
			entitlements: false,
		});
	});
	it("reads the switches from the product-features group, not the build", () => {
		process.env.NEXT_PUBLIC_ENABLE_SSO = "true";
		try {
			expect(resolvePublicRuntimeConfig(reader({})).productFeatures.sso).toBe(
				false,
			);
		} finally {
			delete process.env.NEXT_PUBLIC_ENABLE_SSO;
		}
		expect(
			resolvePublicRuntimeConfig(
				reader({
					"product-features": {
						NEXT_PUBLIC_ENABLE_SUBSCRIPTIONS: "true",
						NEXT_PUBLIC_ENABLE_SSO: " TRUE ",
						NEXT_PUBLIC_ENABLE_ENTITLEMENTS: "yes",
					},
				}),
			).productFeatures,
		).toEqual({ subscriptions: true, sso: true, entitlements: false });
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
