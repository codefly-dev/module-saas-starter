import {
	BrowserAnalytics,
	type BrowserEventName,
	NoopBrowserAnalyticsSink,
} from "./browser";
import { PostHogBrowserSink } from "./posthog-browser";

export type BrowserAnalyticsRuntime = BrowserAnalytics;

export function createBrowserAnalytics({
	mode,
	host,
	apiKey,
	storage,
	route,
	release,
	environment,
}: {
	mode?: string;
	host?: string;
	apiKey?: string;
	storage: Storage;
	route?: string;
	release?: string;
	environment?: string;
}): BrowserAnalyticsRuntime {
	const normalizedMode = (mode ?? "disabled").trim().toLowerCase();
	let sink;
	switch (normalizedMode) {
		case "disabled":
			if (host?.trim() || apiKey?.trim()) {
				throw new Error(
					"PostHog browser configuration is present while product analytics is disabled",
				);
			}
			sink = new NoopBrowserAnalyticsSink();
			break;
		case "posthog":
			if (!host?.trim() || !apiKey?.trim()) {
				throw new Error(
					"PostHog host and project key are required when product analytics is enabled",
				);
			}
			sink = new PostHogBrowserSink({ host, apiKey });
			break;
		default:
			throw new Error(
				"NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE must be disabled or posthog",
			);
	}
	return new BrowserAnalytics({
		sink,
		storage,
		context: { route, release, environment },
	});
}

export type AnalyticsTrack = (
	eventName: BrowserEventName,
	properties?: Record<string, string | number | boolean | null>,
) => Promise<boolean>;
