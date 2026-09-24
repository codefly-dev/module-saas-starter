// instrumentation-client.ts — Sentry browser SDK init.
//
// Loaded by Next.js on the client side. It initializes only when the explicit
// browser error-tracking mode is "sentry" and a matching DSN is present.
//
// The configuration is the deployment's `error-tracking` group, which the root
// layout reads per request and publishes in a <meta> (see
// ERROR_TRACKING_META_NAME). It is never NEXT_PUBLIC_* inlined at build time: an
// image is built once and configured per environment, so a build-time value is
// empty in every deployed image and browser error tracking never started.
//
// Why client + server are separate files in Next 15+: the App
// Router runs different runtimes; Sentry needs to register hooks
// with each. Server-side init lives in instrumentation.ts.

import * as Sentry from "@sentry/nextjs";
import {
	configuredErrorTracking,
	ERROR_TRACKING_META_NAME,
	readBrowserErrorTrackingConfig,
} from "./src/lib/error-tracking";

function initErrorTracking() {
	const published = readBrowserErrorTrackingConfig(document);
	const errorTracking = configuredErrorTracking(published.mode, published.dsn);
	if (!errorTracking.enabled) return;
	Sentry.init({
		dsn: errorTracking.dsn,

		// Environment label for sorting events (dev / staging / prod).
		// Falls back to NODE_ENV which Next sets automatically.
		environment: published.environment || process.env.NODE_ENV,

		// Release tag — couples errors to a specific deploy, so it is a
		// property of the build: set in CI via NEXT_PUBLIC_SENTRY_RELEASE
		// (typically `${git_sha}`).
		release: process.env.NEXT_PUBLIC_SENTRY_RELEASE,

		tracesSampleRate: 0,
		enableLogs: false,
		integrations: (defaultIntegrations) =>
			defaultIntegrations.filter(
				(integration) => integration.name !== "BrowserTracing",
			),

		// Don't send PII by default. Set NEXT_PUBLIC_SENTRY_SEND_PII=1 in the
		// error-tracking group where you want IPs / cookies / search params.
		sendDefaultPii: published.sendDefaultPii === true,

		// Ignore noise that doesn't need a Sentry event. Add cases here
		// as you see them in the Issues feed.
		ignoreErrors: [
			// Browser network blip — happens during tab close.
			"ResizeObserver loop limit exceeded",
			"Non-Error promise rejection captured",
		],
	});
}

// This module can run while the document is still being parsed, before the
// layout's <meta> has arrived; wait for it rather than reading "none".
if (
	document.readyState === "loading" &&
	!document.querySelector(`meta[name="${ERROR_TRACKING_META_NAME}"]`)
) {
	document.addEventListener("DOMContentLoaded", initErrorTracking, {
		once: true,
	});
} else {
	initErrorTracking();
}
