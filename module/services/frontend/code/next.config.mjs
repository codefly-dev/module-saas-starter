import { readdirSync, readFileSync } from "node:fs";
import { withSentryConfig } from "@sentry/nextjs";
import { resolveAccountsBindings } from "./server/accounts-bindings.mjs";

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

/** @type {import('next').NextConfig} */
const nextConfig = {
	output: "standalone",
	reactCompiler: true,
	// Product plugins are additive packages/* workspaces. Discover them instead
	// of requiring each consumer to mutate this protected host config; packages
	// that expose TypeScript under the development condition then participate in
	// Next HMR without a separate dist rebuild race.
	transpilePackages: ["codefly", ...workspacePackageNames],
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

	webpack: {
		treeshake: {
			removeDebugLogging: true,
		},
	},

	// Tunnel Sentry events through the same origin to dodge browser
	// ad-blockers that block sentry.io. /monitoring is a Next route
	// we accept will be exposed publicly.
	tunnelRoute: "/monitoring",
});
