import type { ProductFeatures } from "./public-runtime-config";

export type { ProductFeature, ProductFeatures } from "./public-runtime-config";

/**
 * Whether an optional product surface is presented. The switches are the
 * deployment's `product-features` group, read per request (see
 * readPublicRuntimeConfig) — a presentation choice, never an authorization or
 * entitlement bypass.
 */
export function isProductRouteEnabled(
	path: string,
	features: ProductFeatures,
): boolean {
	if (path === "/admin/entitlements" || path.startsWith("/admin/entitlements/"))
		return features.entitlements;
	if (path === "/admin/billing" || path.startsWith("/admin/billing/"))
		return features.subscriptions;
	if (path === "/admin/sso" || path.startsWith("/admin/sso/"))
		return features.sso;
	return true;
}
