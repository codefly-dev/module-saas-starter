// instrumentation.ts — Server-side Sentry init for Next.js App
// Router. Runs ONCE per server / edge runtime cold-start.
//
// Sentry on the server captures:
//   - Server-component render errors
//   - Route-handler / Server-Action exceptions
//   - Errors thrown during streaming responses
//
// Pairs with instrumentation-client.ts (browser) for full coverage.

import * as Sentry from "@sentry/nextjs";
import { configuredErrorTracking } from "./src/lib/error-tracking";

export async function register() {
	const configuration = configuredErrorTracking(
		process.env.ERROR_TRACKING_MODE,
		process.env.SENTRY_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN,
	);
	if (!configuration.enabled) return;
	const dsn = configuration.dsn;

	if (process.env.NEXT_RUNTIME === "nodejs") {
		Sentry.init({
			dsn,
			environment:
				process.env.SENTRY_ENVIRONMENT ||
				process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT ||
				process.env.NODE_ENV,
			release:
				process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE,
			tracesSampleRate: 0,
			enableLogs: false,
			skipOpenTelemetrySetup: true,
			sendDefaultPii: process.env.SENTRY_SEND_PII === "1",
		});
	}

	// Edge runtime (middleware, edge route handlers). Same DSN; Sentry
	// SDK detects via process.env.NEXT_RUNTIME.
	if (process.env.NEXT_RUNTIME === "edge") {
		Sentry.init({
			dsn,
			environment:
				process.env.SENTRY_ENVIRONMENT ||
				process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT ||
				process.env.NODE_ENV,
			release:
				process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE,
			tracesSampleRate: 0,
			enableLogs: false,
			skipOpenTelemetrySetup: true,
		});
	}
}

// onRequestError — propagates Server Component / Server Action
// errors to Sentry with the request context (URL, headers).
// Required by App Router for full error capture.
export const onRequestError = Sentry.captureRequestError;
