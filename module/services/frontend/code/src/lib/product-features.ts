/** Product presentation switches, not authorization or entitlement bypasses. */
export const productFeatures = {
	subscriptions: process.env.NEXT_PUBLIC_ENABLE_SUBSCRIPTIONS === "true",
	sso: process.env.NEXT_PUBLIC_ENABLE_SSO === "true",
};

export type ProductFeature = keyof typeof productFeatures;

export function isProductRouteEnabled(
	path: string,
	features = productFeatures,
): boolean {
	if (path === "/admin/billing" || path.startsWith("/admin/billing/"))
		return features.subscriptions;
	if (path === "/admin/sso" || path.startsWith("/admin/sso/"))
		return features.sso;
	return true;
}
