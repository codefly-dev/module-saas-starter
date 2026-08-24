// Response security headers for the product frontend, mirroring the marketing
// site's baseline (frame-ancestors 'none' / X-Frame-Options DENY, nosniff,
// Referrer-Policy, COOP, Permissions-Policy) and extending the CSP with the
// cross-origin subresources this app actually loads.
//
// Next bakes `headers()` into the routes manifest at BUILD time, so this reads
// build-time env only. Every widening below is keyed off the same NEXT_PUBLIC_*
// build flag that decides whether the feature ships in the bundle at all, so
// the CSP can never drift out of sync with the code that needs it.
//
// Module Federation is the exception: solutions self-register at RUNTIME (see
// src/solutions/registry.ts), so their origins are not knowable when the
// manifest is built. Solution pages therefore get their CSP from the Node
// proxy (src/proxy.ts), which builds it from a build-time snapshot of the
// inputs below (so it stays in lockstep with every other route's CSP) plus the
// registered manifest origin for that page — so a freshly-registered
// cross-origin remote loads without a rebuild and without a
// FRONTEND_SOLUTION_ORIGINS entry. Today the runtime registers the solution's
// own origin (cross-origin), which this covers. Serving a solution's assets
// same-origin through the host proxy — so `'self'` alone covers it — is the
// intended direction but depends on the gateway serving them unauthenticated
// (tracked separately) and is not yet the default. FRONTEND_SOLUTION_ORIGINS
// remains a build-time escape hatch for origins the host must trust before any
// registration.

const TURNSTILE_ORIGIN = "https://challenges.cloudflare.com";

// The /docs page embeds this API-doc viewer in an iframe; keep in lockstep with
// the hardcoded src in src/app/(dashboard)/docs/api-docs-frame.tsx.
const API_DOCS_VIEWER_ORIGIN = "https://petstore.swagger.io";

/** Absolute http(s) origin, no credentials/path/query/fragment, or throw. */
function toOrigin(value, source) {
	let parsed;
	try {
		parsed = new URL(value);
	} catch {
		throw new Error(`${source} must be an absolute HTTP(S) URL: ${value}`);
	}
	if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
		throw new Error(`${source} must use HTTP or HTTPS: ${value}`);
	}
	if (parsed.username || parsed.password) {
		throw new Error(`${source} cannot contain credentials: ${value}`);
	}
	return parsed.origin;
}

/** Split FRONTEND_SOLUTION_ORIGINS (comma/whitespace separated) into origins. */
export function parseSolutionOrigins(raw) {
	if (!raw) {
		return [];
	}
	const origins = raw
		.split(/[\s,]+/)
		.filter((entry) => entry !== "")
		.map((entry) => toOrigin(entry, "FRONTEND_SOLUTION_ORIGINS entry"));
	return [...new Set(origins)];
}

function posthogOrigin(env) {
	const mode = (env.NEXT_PUBLIC_PRODUCT_ANALYTICS_MODE ?? "")
		.trim()
		.toLowerCase();
	const host = env.NEXT_PUBLIC_POSTHOG_HOST?.trim();
	if (mode !== "posthog" || !host) {
		return null;
	}
	return toOrigin(host, "NEXT_PUBLIC_POSTHOG_HOST");
}

function turnstileEnabled(env) {
	const mode = (env.NEXT_PUBLIC_ABUSE_PROTECTION_MODE ?? "")
		.trim()
		.toLowerCase();
	return mode !== "" && mode !== "disabled";
}

/**
 * Resolve the env-derived inputs the CSP is built from. Split out from assembly
 * so the build-time config and the runtime proxy compose the policy from the
 * SAME values: next.config snapshots these once at build (its `env` block) and
 * the proxy reads that snapshot, adding only the runtime solution origin.
 * Re-resolving from `process.env` at request time would let a solution page's
 * CSP drift from the build-inlined analytics/allowlist hosts the browser
 * actually calls, silently blocking them on solution pages alone.
 * @param {Record<string, string | undefined>} [env]
 */
export function resolveCspInputs(env = process.env) {
	return {
		solutionOrigins: parseSolutionOrigins(env.FRONTEND_SOLUTION_ORIGINS),
		analyticsOrigin: posthogOrigin(env),
		turnstile: turnstileEnabled(env),
	};
}

/**
 * @param {ReturnType<typeof resolveCspInputs>} inputs
 * @param {string[]} [runtimeSolutionOrigins] Origins derived at request time
 *   from the runtime solution registry (see src/proxy.ts). Merged with the
 *   build-time FRONTEND_SOLUTION_ORIGINS allowlist.
 */
export function contentSecurityPolicyFromInputs(
	inputs,
	runtimeSolutionOrigins = [],
) {
	const solutionOrigins = [
		...new Set([...inputs.solutionOrigins, ...runtimeSolutionOrigins]),
	];
	const analyticsOrigin = inputs.analyticsOrigin;
	const turnstile = inputs.turnstile;

	// Remote solution scripts execute in this origin; the MF runtime fetches
	// their manifest/chunks. Turnstile injects its challenge script.
	const scriptSrc = ["'self'", "'unsafe-inline'", ...solutionOrigins];
	// Same-origin API (proxied via rewrites) + the Sentry tunnel; MF manifest
	// fetches; PostHog ingestion; Turnstile.
	const connectSrc = ["'self'", ...solutionOrigins];
	if (analyticsOrigin) {
		connectSrc.push(analyticsOrigin);
	}
	if (turnstile) {
		scriptSrc.push(TURNSTILE_ORIGIN);
		connectSrc.push(TURNSTILE_ORIGIN);
	}

	// The /docs viewer is always embedded; Turnstile renders its challenge in a
	// Cloudflare-hosted iframe when enabled.
	const frameSrc = ["'self'", API_DOCS_VIEWER_ORIGIN];
	if (turnstile) {
		frameSrc.push(TURNSTILE_ORIGIN);
	}

	return [
		"default-src 'self'",
		"base-uri 'self'",
		`connect-src ${connectSrc.join(" ")}`,
		"font-src 'self'",
		"form-action 'self'",
		`frame-src ${frameSrc.join(" ")}`,
		"frame-ancestors 'none'",
		// Avatars are user-supplied absolute URLs (Profile → Avatar URL), so any
		// https image origin must load; data: covers inline/blob previews.
		"img-src 'self' data: https:",
		"object-src 'none'",
		`script-src ${scriptSrc.join(" ")}`,
		"style-src 'self' 'unsafe-inline'",
		"upgrade-insecure-requests",
	].join("; ");
}

/**
 * @param {Record<string, string | undefined>} [env]
 * @param {string[]} [runtimeSolutionOrigins]
 */
export function contentSecurityPolicy(
	env = process.env,
	runtimeSolutionOrigins = [],
) {
	return contentSecurityPolicyFromInputs(
		resolveCspInputs(env),
		runtimeSolutionOrigins,
	);
}

// The constant hardening headers, minus the CSP. Solution pages (/s/:id) omit
// the CSP here and receive it from the Node proxy instead, so the build-time
// manifest never emits a second, narrower CSP that would intersect with (and
// defeat) the runtime-derived one.
export function baselineSecurityHeaders() {
	return [
		{ key: "Cross-Origin-Opener-Policy", value: "same-origin" },
		{
			key: "Permissions-Policy",
			value: "camera=(), microphone=(), geolocation=()",
		},
		{ key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
		{ key: "X-Content-Type-Options", value: "nosniff" },
		{ key: "X-Frame-Options", value: "DENY" },
	];
}

/** @param {Record<string, string | undefined>} [env] */
export function securityHeaders(env = process.env) {
	return [
		{ key: "Content-Security-Policy", value: contentSecurityPolicy(env) },
		...baselineSecurityHeaders(),
	];
}
