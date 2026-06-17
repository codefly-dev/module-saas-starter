import { withSentryConfig } from "@sentry/nextjs";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  // Same-origin API proxy for local dev / e2e, where there is no auth-sidecar
  // gateway in front of the frontend. The browser only ever talks to the
  // frontend's own origin (so NEXT_PUBLIC_API_* point at it), and the Next
  // server proxies API traffic to the api service. This keeps auth cookies
  // FIRST-PARTY, so a full page load / reload re-establishes the session — a
  // cross-origin frontend→api setup drops the cookie and bounces to login.
  //
  // Only registered when the internal api addresses are provided (dev/e2e):
  //   API_REST_INTERNAL    — api REST endpoint    (e.g. http://localhost:10122)
  //   API_CONNECT_INTERNAL — api Connect endpoint (e.g. http://localhost:16910)
  // In production the gateway fronts these paths, the env vars are unset, and
  // these rewrites are simply not registered.
  async rewrites() {
    const apiRest = process.env.API_REST_INTERNAL;
    const apiConnect = process.env.API_CONNECT_INTERNAL;
    const rules = [];
    if (apiRest) {
      rules.push({ source: "/v1/:path*", destination: `${apiRest}/v1/:path*` });
    }
    if (apiConnect) {
      // Connect-ES service paths, e.g. /customers.UserService/ListUsers.
      rules.push({ source: "/customers.:path*", destination: `${apiConnect}/customers.:path*` });
    }
    return rules;
  },
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
