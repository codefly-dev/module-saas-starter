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

export async function register() {
	const dsn = process.env.SENTRY_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN;
	if (!dsn) return;

	if (process.env.NEXT_RUNTIME === "nodejs") {
		Sentry.init({
			dsn,
			environment:
				process.env.SENTRY_ENVIRONMENT ||
				process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT ||
				process.env.NODE_ENV,
			release:
				process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE,
			tracesSampleRate: parseFloat(
				process.env.SENTRY_TRACES_SAMPLE_RATE || "0.1",
			),
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
			tracesSampleRate: parseFloat(
				process.env.SENTRY_TRACES_SAMPLE_RATE || "0.1",
			),
		});
	}
}

// onRequestError — propagates Server Component / Server Action
// errors to Sentry with the request context (URL, headers).
// Required by App Router for full error capture.
export const onRequestError = Sentry.captureRequestError;
