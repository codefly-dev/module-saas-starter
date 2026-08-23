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
// manifest is built. A cross-origin remote therefore only loads under this CSP
// if its origin is listed at build time via FRONTEND_SOLUTION_ORIGINS. A remote
// served from the host origin needs no configuration.

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

export function contentSecurityPolicy(env = process.env) {
	const solutionOrigins = parseSolutionOrigins(env.FRONTEND_SOLUTION_ORIGINS);
	const analyticsOrigin = posthogOrigin(env);
	const turnstile = turnstileEnabled(env);

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

export function securityHeaders(env = process.env) {
	return [
		{ key: "Content-Security-Policy", value: contentSecurityPolicy(env) },
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
