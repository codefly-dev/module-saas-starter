import { withSentryConfig } from "@sentry/nextjs";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
};

// Sentry build-time plugin:
//   - Source-map upload when SENTRY_AUTH_TOKEN is set in CI (errors
//     in the Issues feed get mapped back to TypeScript stack frames).
//   - Tunneling — proxies Sentry events through /monitoring so an
//     ad-blocker on a customer's browser doesn't drop them.
//   - Server-side tree-shaking of debug code.
//
// Wrapper is a no-op without the env vars below; safe to leave in
// place for local dev where SENTRY_AUTH_TOKEN / SENTRY_ORG are unset.
//
// Required CI env to actually upload source maps:
//   SENTRY_ORG          — sentry org slug (e.g. "saas-starter")
//   SENTRY_PROJECT      — sentry project slug (e.g. "frontend")
//   SENTRY_AUTH_TOKEN   — auth token w/ project-write scope (in
//                         secrets manager, never committed)
//
// At runtime (browser + server), separately:
//   NEXT_PUBLIC_SENTRY_DSN  — client-side DSN
//   SENTRY_DSN              — server-side DSN (often same value)
//   *_SENTRY_RELEASE        — git sha for deploy correlation
//   *_SENTRY_ENVIRONMENT    — "production" | "staging" | etc.
//
// See instrumentation.ts and instrumentation-client.ts for the
// runtime init.
export default withSentryConfig(nextConfig, {
  org: process.env.SENTRY_ORG,
  project: process.env.SENTRY_PROJECT,
  authToken: process.env.SENTRY_AUTH_TOKEN,

  // Only print upload progress when CI sets the token; quiet in dev.
  silent: !process.env.SENTRY_AUTH_TOKEN,

  // Hides the Sentry SDK from public source maps so an attacker
  // peeking at the JS bundle doesn't get a SDK fingerprint they
  // can grep CVEs against. Off-by-default; set to true to enable.
  hideSourceMaps: true,

  // Disable source-map upload locally (no auth token = no upload
  // attempt = no log noise).
  disableLogger: true,

  // Tunnel Sentry events through the same origin to dodge browser
  // ad-blockers that block sentry.io. /monitoring is a Next route
  // we accept will be exposed publicly.
  tunnelRoute: "/monitoring",
});
