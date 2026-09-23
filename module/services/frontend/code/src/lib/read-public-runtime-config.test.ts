import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("next/server", () => ({ connection: vi.fn(async () => undefined) }));

import { readPublicRuntimeConfig } from "./read-public-runtime-config";

const set: string[] = [];
function provision(
	namespace: string,
	group: string,
	key: string,
	value: string,
) {
	const name = `${namespace}__${group.toUpperCase().replaceAll("-", "_")}__${key}`;
	process.env[name] = value;
	set.push(name);
}
const plain = (group: string, key: string, value: string) =>
	provision("CODEFLY__WORKSPACE_CONFIGURATION", group, key, value);
const secret = (group: string, key: string, value: string) =>
	provision("CODEFLY__WORKSPACE_SECRET_CONFIGURATION", group, key, value);

afterEach(() => {
	for (const name of set.splice(0)) delete process.env[name];
});

describe("readPublicRuntimeConfig", () => {
	it("reads every browser-facing switch from its group in the running process", async () => {
		plain("product-features", "NEXT_PUBLIC_ENABLE_SSO", "true");
		plain(
			"product-features",
			"NEXT_PUBLIC_COLLECTION_CONTENT_RESOURCE",
			"example-records",
		);
		plain("abuse-protection", "NEXT_PUBLIC_ABUSE_PROTECTION_MODE", "turnstile");
		plain("abuse-protection", "NEXT_PUBLIC_TURNSTILE_SITE_KEY", "site-key");
		plain("product-analytics", "NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE", "posthog");
		plain(
			"product-analytics",
			"NEXT_PUBLIC_POSTHOG_HOST",
			"https://eu.i.posthog.com",
		);
		plain("error-tracking", "NEXT_PUBLIC_ERROR_TRACKING_MODE", "sentry");

		const config = await readPublicRuntimeConfig();
		expect(config.productFeatures).toEqual({
			subscriptions: false,
			sso: true,
			entitlements: false,
		});
		expect(config.collectionContentResource).toBe("example-records");
		expect(config.abuseProtection).toEqual({
			mode: "turnstile",
			siteKey: "site-key",
		});
		expect(config.productAnalytics).toMatchObject({
			mode: "posthog",
			host: "https://eu.i.posthog.com",
		});
		expect(config.errorTracking.mode).toBe("sentry");
	});

	it("takes the browser DSN from the secret half, where a restricted render puts it", async () => {
		secret(
			"error-tracking",
			"NEXT_PUBLIC_SENTRY_DSN",
			"https://public@example.invalid/1",
		);
		expect((await readPublicRuntimeConfig()).errorTracking.dsn).toBe(
			"https://public@example.invalid/1",
		);
	});

	it("never hands the browser any other secret-namespace value", async () => {
		secret("product-analytics", "NEXT_PUBLIC_POSTHOG_KEY", "must-not-leak");
		secret(
			"abuse-protection",
			"NEXT_PUBLIC_TURNSTILE_SITE_KEY",
			"must-not-leak",
		);
		const config = await readPublicRuntimeConfig();
		expect(JSON.stringify(config)).not.toContain("must-not-leak");
	});

	it("is an unconfigured deployment when the groups are empty", async () => {
		expect(await readPublicRuntimeConfig()).toEqual({
			productFeatures: {
				subscriptions: false,
				sso: false,
				entitlements: false,
			},
			collectionContentResource: undefined,
			abuseProtection: { mode: undefined, siteKey: undefined },
			productAnalytics: { mode: undefined, host: undefined, apiKey: undefined },
			errorTracking: {
				mode: undefined,
				dsn: undefined,
				environment: undefined,
				sendDefaultPii: false,
			},
		});
	});
});
