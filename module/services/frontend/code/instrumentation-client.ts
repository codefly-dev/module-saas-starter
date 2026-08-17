// instrumentation-client.ts — Sentry browser SDK init.
//
// Loaded by Next.js on the client side. It initializes only when the explicit
// browser error-tracking mode is "sentry" and a matching DSN is present.
//
// Why client + server are separate files in Next 15+: the App
// Router runs different runtimes; Sentry needs to register hooks
// with each. Server-side init lives in instrumentation.ts.

import * as Sentry from "@sentry/nextjs";
import { configuredErrorTracking } from "./src/lib/error-tracking";

const errorTracking = configuredErrorTracking(
	process.env.NEXT_PUBLIC_ERROR_TRACKING_MODE,
	process.env.NEXT_PUBLIC_SENTRY_DSN,
);

if (errorTracking.enabled) {
	Sentry.init({
		dsn: errorTracking.dsn,

		// Environment label for sorting events (dev / staging / prod).
		// Falls back to NODE_ENV which Next sets automatically.
		environment:
			process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT || process.env.NODE_ENV,

		// Release tag — couples errors to a specific deploy. Set in CI
		// via NEXT_PUBLIC_SENTRY_RELEASE (typically `${git_sha}`).
		release: process.env.NEXT_PUBLIC_SENTRY_RELEASE,

		tracesSampleRate: 0,
		enableLogs: false,
		integrations: (defaultIntegrations) =>
			defaultIntegrations.filter(
				(integration) => integration.name !== "BrowserTracing",
			),

		// Don't send PII by default. Set NEXT_PUBLIC_SENTRY_SEND_PII=1
		// in environments where you want IPs / cookies / search params.
		sendDefaultPii: process.env.NEXT_PUBLIC_SENTRY_SEND_PII === "1",

		// Ignore noise that doesn't need a Sentry event. Add cases here
		// as you see them in the Issues feed.
		ignoreErrors: [
			// Browser network blip — happens during tab close.
			"ResizeObserver loop limit exceeded",
			"Non-Error promise rejection captured",
		],
	});
}
