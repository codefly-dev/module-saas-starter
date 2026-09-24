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
	if (process.env.NEXT_RUNTIME === "nodejs") {
		// Startup gate for the single product API path. The frontend has no
		// direct-Accounts fallback, so a composition that resolves no
		// auth-gateway/rest must refuse to serve rather than answer product API
		// routes from the Next app itself. Imported here so the resolver stays out
		// of the edge instrumentation bundle.
		const { resolveAccountsBindings } = await import(
			"./server/accounts-bindings.mjs"
		);
		resolveAccountsBindings();
	}

	// On Node the deployment's `error-tracking` group is read through the Codefly
	// SDK: a deployed pod carries it under its CODEFLY__ name, never as a bare
	// ERROR_TRACKING_MODE / SENTRY_DSN, so reading only process.env left server
	// error tracking off in every deployment. The bare variables are the source
	// only for a process the group does not reach (started outside Codefly, or
	// the edge runtime, which cannot load the SDK).
	//
	// One source or the other, never a key-by-key mix: a mode from the group and
	// a DSN from a stray variable is a configuration nobody wrote, and it refuses
	// to boot ("DSN is present while disabled").
	const group =
		process.env.NEXT_RUNTIME === "nodejs"
			? await import("codefly").then(
					({ getWorkspaceValue }) =>
						(key: string) =>
							getWorkspaceValue("error-tracking", key)?.trim() || undefined,
				)
			: () => undefined;
	const fromGroup = group("ERROR_TRACKING_MODE") !== undefined;
	const setting = (key: string) =>
		fromGroup ? group(key) : process.env[key];

	const configuration = configuredErrorTracking(
		setting("ERROR_TRACKING_MODE"),
		setting("SENTRY_DSN") || setting("NEXT_PUBLIC_SENTRY_DSN"),
	);
	if (!configuration.enabled) return;
	const dsn = configuration.dsn;

	if (process.env.NEXT_RUNTIME === "nodejs") {
		Sentry.init({
			dsn,
			environment:
				setting("SENTRY_ENVIRONMENT") ||
				setting("NEXT_PUBLIC_SENTRY_ENVIRONMENT") ||
				process.env.NODE_ENV,
			release:
				process.env.SENTRY_RELEASE || process.env.NEXT_PUBLIC_SENTRY_RELEASE,
			tracesSampleRate: 0,
			enableLogs: false,
			skipOpenTelemetrySetup: true,
			sendDefaultPii: setting("SENTRY_SEND_PII") === "1",
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
