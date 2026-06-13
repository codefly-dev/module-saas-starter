// instrumentation-client.ts — Sentry browser SDK init.
//
// Loaded by Next.js on the client side. Only fires when
// NEXT_PUBLIC_SENTRY_DSN is set (no-op in dev / for self-hosted
// runs that don't want to ship telemetry).
//
// Why client + server are separate files in Next 15+: the App
// Router runs different runtimes; Sentry needs to register hooks
// with each. Server-side init lives in instrumentation.ts.

import * as Sentry from "@sentry/nextjs";

const dsn = process.env.NEXT_PUBLIC_SENTRY_DSN;

if (dsn) {
  Sentry.init({
    dsn,

    // Environment label for sorting events (dev / staging / prod).
    // Falls back to NODE_ENV which Next sets automatically.
    environment:
      process.env.NEXT_PUBLIC_SENTRY_ENVIRONMENT ||
      process.env.NODE_ENV,

    // Release tag — couples errors to a specific deploy. Set in CI
    // via NEXT_PUBLIC_SENTRY_RELEASE (typically `${git_sha}`).
    release: process.env.NEXT_PUBLIC_SENTRY_RELEASE,

    // Performance sampling. 0.1 = 10% of transactions traced. Tune
    // up in staging, down in prod when volume hits the plan limit.
    // 0 = no perf data; errors-only.
    tracesSampleRate: parseFloat(
      process.env.NEXT_PUBLIC_SENTRY_TRACES_SAMPLE_RATE || "0.1",
    ),

    // Browser-only integrations:
    integrations: [
      // Auto-instruments fetch/xhr and stamps W3C `traceparent` +
      // `baggage` headers on outgoing requests, so a browser span
      // becomes the parent of the corresponding gRPC server span on
      // the api side (otelgrpc reads the same headers from incoming
      // metadata — see grpc_gen.go's wooltel.GRPCServerOptions).
      Sentry.browserTracingIntegration(),
    ],

    // Sentry only injects traceparent for same-origin + localhost by
    // default. Connect-ES talks to the gateway on a different host,
    // so we explicitly opt that origin in. Same-origin (/) stays in.
    tracePropagationTargets: [
      "localhost",
      /^\//,
      ...(process.env.NEXT_PUBLIC_API_CONNECT
        ? [process.env.NEXT_PUBLIC_API_CONNECT]
        : []),
      ...(process.env.NEXT_PUBLIC_API_REST
        ? [process.env.NEXT_PUBLIC_API_REST]
        : []),
    ],

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

// Required export for App Router routing instrumentation. No-op
// when DSN is unset (Sentry.captureRouterTransitionStart is a stub
// in that case).
export const onRouterTransitionStart = Sentry.captureRouterTransitionStart;
