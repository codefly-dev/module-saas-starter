/**
 * The browser-safe configuration this deployment runs with, read per request
 * from the Codefly workspace groups that own each value (see
 * readPublicRuntimeConfig) and handed to the client as props — never inlined
 * from `NEXT_PUBLIC_*` at build time. An image is built once and configured per
 * environment, so a build-time value is whatever the build saw, which for a
 * deployed image is nothing: every switch below read "off" in every cell.
 *
 * The keys keep their historical `NEXT_PUBLIC_*` names so an existing group
 * keeps working; only where they are read from changed.
 */

/** Product presentation switches, not authorization or entitlement bypasses. */
export interface ProductFeatures {
	subscriptions: boolean;
	sso: boolean;
	entitlements: boolean;
}

export type ProductFeature = keyof ProductFeatures;

export interface PublicRuntimeConfig {
	productFeatures: ProductFeatures;
	/**
	 * The permission resource type this deployment's collection content is
	 * governed by. Unset means undeclared: the host holds no domain content, so
	 * it has no noun of its own to fall back on.
	 */
	collectionContentResource?: string;
	/** Raw values; `configuredAbuseProtection` validates them where used. */
	abuseProtection: { mode?: string; siteKey?: string };
	/** Raw values; `createBrowserAnalytics` validates them where used. */
	productAnalytics: { mode?: string; host?: string; apiKey?: string };
	/** Raw values; `configuredErrorTracking` validates them where used. */
	errorTracking: {
		mode?: string;
		dsn?: string;
		environment?: string;
		sendDefaultPii: boolean;
	};
}

/** Reads one key of one workspace group; `undefined` when unset. */
export type WorkspaceReader = (
	group: string,
	key: string,
) => string | undefined;

/**
 * What a client component sees with no provider above it: every optional
 * surface off, nothing declared. The same as an unconfigured deployment.
 */
export const UNCONFIGURED_PUBLIC_RUNTIME_CONFIG: PublicRuntimeConfig = {
	productFeatures: { subscriptions: false, sso: false, entitlements: false },
	abuseProtection: {},
	productAnalytics: {},
	errorTracking: { sendDefaultPii: false },
};

const trimmed = (value: string | undefined) => {
	const text = value?.trim();
	return text ? text : undefined;
};

/**
 * Resolve the public configuration from the groups that own it. Pure over the
 * reader, so the server read and the tests share one mapping.
 */
export function resolvePublicRuntimeConfig(
	read: WorkspaceReader,
): PublicRuntimeConfig {
	const features = (key: string) =>
		read("product-features", key)?.trim().toLowerCase() === "true";
	return {
		productFeatures: {
			subscriptions: features("NEXT_PUBLIC_ENABLE_SUBSCRIPTIONS"),
			sso: features("NEXT_PUBLIC_ENABLE_SSO"),
			entitlements: features("NEXT_PUBLIC_ENABLE_ENTITLEMENTS"),
		},
		collectionContentResource: trimmed(
			read("product-features", "NEXT_PUBLIC_COLLECTION_CONTENT_RESOURCE"),
		),
		abuseProtection: {
			mode: trimmed(
				read("abuse-protection", "NEXT_PUBLIC_ABUSE_PROTECTION_MODE"),
			),
			siteKey: trimmed(
				read("abuse-protection", "NEXT_PUBLIC_TURNSTILE_SITE_KEY"),
			),
		},
		productAnalytics: {
			mode: trimmed(
				read("product-analytics", "NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE"),
			),
			host: trimmed(read("product-analytics", "NEXT_PUBLIC_POSTHOG_HOST")),
			apiKey: trimmed(read("product-analytics", "NEXT_PUBLIC_POSTHOG_KEY")),
		},
		errorTracking: {
			mode: trimmed(read("error-tracking", "NEXT_PUBLIC_ERROR_TRACKING_MODE")),
			dsn: trimmed(read("error-tracking", "NEXT_PUBLIC_SENTRY_DSN")),
			environment: trimmed(
				read("error-tracking", "NEXT_PUBLIC_SENTRY_ENVIRONMENT"),
			),
			sendDefaultPii:
				read("error-tracking", "NEXT_PUBLIC_SENTRY_SEND_PII")?.trim() === "1",
		},
	};
}
