import { readdirSync, readFileSync } from "node:fs";
import { withSentryConfig } from "@sentry/nextjs";
import { getCurrentFixture } from "codefly";
import { resolveAccountsBindings } from "./server/accounts-bindings.mjs";
import {
	baselineSecurityHeaders,
	securityHeaders,
} from "./server/security-headers.mjs";

const workspacePackageNames = readdirSync(
	new URL("./packages", import.meta.url),
	{
		withFileTypes: true,
	},
)
	.filter((entry) => entry.isDirectory())
	.flatMap((entry) => {
		try {
			const manifest = JSON.parse(
				readFileSync(
					new URL(`./packages/${entry.name}/package.json`, import.meta.url),
					"utf8",
				),
			);
			return typeof manifest.name === "string" ? [manifest.name] : [];
		} catch {
			return [];
		}
	});

// Legal placeholders (see src/lib/legal-config.ts) are a DEV-ONLY affordance:
// they keep the required terms gate usable in the local fixture stack, which
// ships no operator legal content. The trigger must be SAFE-BY-DEFAULT — a real
// deploy that forgets to configure content must fall to the closed gate, never
// silently ship placeholder terms.
//
// Codefly's fixture selection is that boundary: `--fixture <name>` (and the E2E
// runner) set the current fixture; a real deploy never runs under one. We surface
// it to the browser bundle as NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER, because
// NEXT_PUBLIC_* vars are inlined at BUILD time and the fixture is not itself a
// NEXT_PUBLIC_ var. An explicit NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER in the
// environment still wins, so operators can force either state.
function fixtureActive() {
	return getCurrentFixture().trim() !== "";
}

const legalDevPlaceholder =
	process.env.NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER ??
	(fixtureActive() ? "true" : "");

/** @type {import('next').NextConfig} */
const nextConfig = {
	output: "standalone",
	reactCompiler: true,
	// Inlined into client + server bundles at build time; read by legal-config.ts
	// to decide whether dev legal placeholders apply. Default derives from the
	// fixture boundary above, so real deploys stay safe-by-default.
	env: {
		NEXT_PUBLIC_LEGAL_DEV_PLACEHOLDER: legalDevPlaceholder,
	},
	// Product plugins are additive packages/* workspaces. Discover them instead
	// of requiring each consumer to mutate this protected host config; packages
	// that expose TypeScript under the development condition then participate in
	// Next HMR without a separate dist rebuild race.
	transpilePackages: ["codefly", ...workspacePackageNames],
	// Anti-clickjacking + CSP baseline for the authenticated product surface.
	// The CSP allowlist for cross-origin subresources (analytics, bot-protection)
	// is derived from build-time config; see server/security-headers.mjs.
	//
	// Solution pages (/s/:id) are the exception: their Module Federation remote
	// registers at runtime, so its origin cannot be baked in here. Those routes
	// get the constant hardening headers at build time and their CSP from the
	// Node proxy (src/proxy.ts) per request. Emitting a build-time CSP for them
	// too would produce a second, narrower CSP the browser intersects with the
	// runtime one — re-blocking the remote.
	async headers() {
		return [
			{ source: "/((?!s/).*)", headers: securityHeaders() },
			{ source: "/s/:path*", headers: baselineSecurityHeaders() },
		];
	},
	// The frontend is the module's public product entry. The browser only talks
	// to this origin; Next proxies API traffic to auth-sidecar/rest, which
	// enforces the generated route/auth policy before Accounts. This keeps auth
	// cookies first-party while preserving the backend trust boundary.
	//
	// Complete Codefly runs resolve auth-sidecar through the SDK. Isolated
	// Playwright runs may provide direct API_* fallbacks because they
	// intentionally do not start the module graph.
	async rewrites() {
		const { rest: apiRest, connect: apiConnect } = resolveAccountsBindings();
		const rules = [];
		if (apiRest) {
			rules.push({ source: "/v1/:path*", destination: `${apiRest}/v1/:path*` });
		}
		if (apiConnect) {
			// Connect-ES service paths, e.g. /saas.accounts.v1.UserService/ListUsers.
			// Keep the generated service and method as separate path segments. A
			// single `:path*` after the package dot does not match the following `/`
			// under Next 16, so the request falls through to the frontend as a 404.
			rules.push({
				source: "/saas.accounts.v1.:service/:method",
				destination: `${apiConnect}/saas.accounts.v1.:service/:method`,
			});
		}
		return rules;
	},
};

// Sentry build-time plugin:
//   - Source-map upload when SENTRY_AUTH_TOKEN is set in CI (errors
//     in the Issues feed get mapped back to TypeScript stack frames).
//   - Tunneling — proxies Sentry events through /monitoring so an
//     ad-blocker on a customer's browser doesn't drop them.
//   - Webpack tree-shaking of debug code when Webpack is selected.
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
const sentryErrorTrackingConfig = withSentryConfig(nextConfig, {
	org: process.env.SENTRY_ORG,
	project: process.env.SENTRY_PROJECT,
	authToken: process.env.SENTRY_AUTH_TOKEN,

	// Only print upload progress when CI sets the token; quiet in dev.
	silent: !process.env.SENTRY_AUTH_TOKEN,

	// Hides the Sentry SDK from public source maps so an attacker
	// peeking at the JS bundle doesn't get a SDK fingerprint they
	// can grep CVEs against. Off-by-default; set to true to enable.
	hideSourceMaps: true,

	webpack: {
		treeshake: {
			removeDebugLogging: true,
			removeTracing: true,
		},
	},
	routeManifestInjection: false,
	suppressOnRouterTransitionStartWarning: true,

	// Tunnel Sentry events through the same origin to dodge browser
	// ad-blockers that block sentry.io. /monitoring is a Next route
	// we accept will be exposed publicly.
	tunnelRoute: "/monitoring",
});

// The Sentry wrapper injects navigation trace metadata independently of the
// runtime sample rate. Error-only mode must not propagate that trace context.
if (sentryErrorTrackingConfig.experimental) {
	delete sentryErrorTrackingConfig.experimental.clientTraceMetadata;
}

export default sentryErrorTrackingConfig;
