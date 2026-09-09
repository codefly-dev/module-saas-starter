// Next.js proxy — runs on every request matching the matcher
// config at the bottom of this file.
//
// Job: if the request is for a protected page and the user isn't
// authenticated, redirect to /auth/login with a `next` query param so
// we can bounce back after sign-in.
//
// We detect "authenticated" by looking for the `codefly_session`
// cookie set by the AuthProvider on successful login. This is a soft
// check — the sidecar is still the authoritative gatekeeper on any
// backend call. The middleware is purely for UX.
//
// Public pages (login, callback, landing, health) bypass the check.

import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";
import {
	type CodeflyGatewayContext,
	resolveCodeflyGatewayContext,
} from "@/lib/codefly-gateway-context";
import { contentSecurityPolicyFromInputs } from "../server/security-headers.mjs";

const PRODUCT_API_PREFIXES = ["/v1/", "/saas.accounts.v1."] as const;
const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";
const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

function isProductAPI(pathname: string): boolean {
	return PRODUCT_API_PREFIXES.some((prefix) => pathname.startsWith(prefix));
}

// Next derives `nextUrl`'s protocol and host from the internal request URL and
// ignores `x-forwarded-*`, so behind a TLS-terminating ingress `nextUrl.protocol`
// is the pod's plaintext `http:` rather than the browser's `https:` — and
// Accounts rejects a non-loopback `http` public origin, which would leave OAuth
// broken. The ingress sets the forwarded pair from the real client connection;
// prefer each, falling back to `nextUrl` (local dev, direct-to-pod requests).
export function publicRequestOrigin(req: NextRequest): string {
	const forwardedProto = req.headers
		.get("x-forwarded-proto")
		?.split(",")[0]
		?.trim();
	const forwardedHost = req.headers
		.get("x-forwarded-host")
		?.split(",")[0]
		?.trim();
	const protocol = forwardedProto ? `${forwardedProto}:` : req.nextUrl.protocol;
	const host = forwardedHost || req.nextUrl.host;
	return `${protocol}//${host}`;
}

export function trustedGatewayRequestHeaders(
	req: NextRequest,
	context: CodeflyGatewayContext | undefined,
): Headers | undefined {
	if (!isProductAPI(req.nextUrl.pathname)) return undefined;
	if (!context) return undefined;

	const headers = new Headers(req.headers);
	// Caller-supplied trust headers are never forwarded. The server replaces
	// them from Codefly's secret configuration and the actual browser origin.
	headers.delete(INTERNAL_TOKEN_HEADER);
	headers.delete(PUBLIC_ORIGIN_HEADER);
	headers.set(INTERNAL_TOKEN_HEADER, context.internalToken);
	headers.set(PUBLIC_ORIGIN_HEADER, context.publicOrigin);
	return headers;
}

const PUBLIC_PATHS = [
	"/",
	"/auth/login",
	"/auth/callback",
	"/auth/mfa",
	"/auth/magic-link",
	"/auth/logout",
	"/invitations/accept",
	"/invitations/accept/api",
	"/waitlist",
	"/waitlist/verify",
	"/waitlist/verify/api",
	"/legal/terms",
	"/legal/privacy",
	"/health",
	"/favicon.ico",
];

// Build-time snapshot of the env-derived CSP inputs, inlined by next.config's
// `env` block. Reading this constant — not re-resolving process.env per request
// — keeps a solution page's CSP in lockstep with the build-time policy on every
// other route and with the analytics/allowlist hosts the client bundle was
// built to call. Absent only if the build failed to inline it, which must fail
// loudly (never silently ship a narrowed CSP that drops those hosts).
function baselineCspInputs(): {
	solutionOrigins: string[];
	analyticsOrigin: string | null;
	turnstile: boolean;
	isDev: boolean;
} {
	const snapshot = process.env.SOLUTION_CSP_INPUTS;
	if (!snapshot) {
		throw new Error(
			"SOLUTION_CSP_INPUTS is unset; next.config must snapshot the CSP inputs at build time",
		);
	}
	return JSON.parse(snapshot);
}

// A solution's Module Federation remote registers at RUNTIME (see
// src/solutions/registry.ts), so the build-time CSP in next.config cannot know
// its origin. Next runs this proxy in a context whose module singletons and
// globals are NOT shared with route handlers or pages (see the Next "proxy"
// docs: "you should not attempt relying on shared modules or globals"), so it
// cannot read the in-process registry the register endpoint and solution page
// share. Instead it asks the host over the local solutions listing — which does
// run in that shared context — and admits every registered remote's origin,
// letting a freshly registered cross-origin remote load with no rebuild and no
// FRONTEND_SOLUTION_ORIGINS entry.
//
// Every authenticated document gets the full registered set, not only /s/:id.
// A CSP is document-scoped, and the sidebar reaches a solution through
// client-side navigation (next/link), which swaps the RSC payload but keeps the
// policy of the document the user started in — typically the dashboard.
// Widening only /s/:id therefore left the manifest fetch blocked by that
// starting document's self-only connect-src (Module Federation RUNTIME-003)
// until a hard reload landed a fresh document on /s/:id. Any authenticated page
// can navigate into any solution, so every authenticated document must already
// permit every registered remote (#545). "Authenticated" is decided by the
// session cookie, not the route: "/" is a public path and also the signed-in
// home the login flow lands on (a full document load), so keying on the path
// would leave that very document self-only. Documents served without a session
// stay self-only and skip the lookup — nothing unauthenticated hosts a remote.
//
// The listing is fetched over loopback at the port THIS server binds — read
// from PORT with the same fallback Next's standalone server uses, so it always
// matches the actual listener. It must NOT be derived from the request origin
// (the client-controlled Host header): routing a server-side fetch through Host
// is an SSRF sink, and behind a TLS-terminating ingress the "self" origin is the
// public hostname, so the request would egress back out instead of staying
// local. A slow or wedged listener must not stall the page, so the fetch is
// bounded; on any failure the CSP falls back to self-only and the cause is
// logged rather than swallowed, since a silent fallback is indistinguishable
// from the very bug this fixes.
async function registeredSolutionOrigins(pathname: string): Promise<string[]> {
	// Mirror Next's standalone server: parseInt(PORT, 10) || 3000, so an unset,
	// empty, or non-numeric PORT resolves to the same port the server bound.
	const port = Number.parseInt(process.env.PORT ?? "", 10) || 3000;
	const listingUrl = new URL(
		"/api/solutions/register",
		`http://127.0.0.1:${port}`,
	);
	let solutions: Array<{ id: string; frontend?: { manifestUrl?: string } }>;
	try {
		const listing = await fetch(listingUrl, {
			headers: { accept: "application/json" },
			signal: AbortSignal.timeout(2000),
		});
		if (!listing.ok) {
			console.error(
				`solution CSP: registry listing responded ${listing.status} path=${pathname}`,
			);
			return [];
		}
		({ solutions } = await listing.json());
	} catch (err) {
		console.error(
			`solution CSP: registry listing unavailable path=${pathname}`,
			err,
		);
		return [];
	}
	// manifestUrl is validated as an absolute http(s) URL at registration. Two
	// solutions served from one origin collapse to a single source expression.
	const origins = new Set<string>();
	for (const solution of solutions ?? []) {
		const manifestUrl = solution.frontend?.manifestUrl;
		if (manifestUrl) {
			origins.add(new URL(manifestUrl).origin);
		}
	}
	return [...origins];
}

// Per-request CSP nonce. Built from Web Crypto so it works on either runtime.
// Every HTML response threads this into the request headers (so Next stamps it
// onto the framework inline scripts) and echoes it on the response CSP, letting
// script-src drop 'unsafe-inline' — an injected inline <script> without the
// nonce is refused by the browser.
function mintNonce(): string {
	const bytes = new Uint8Array(16);
	crypto.getRandomValues(bytes);
	let binary = "";
	for (const byte of bytes) {
		binary += String.fromCharCode(byte);
	}
	return btoa(binary);
}

// Attach the nonce'd CSP to a pass-through response: the nonce goes on the
// forwarded request headers (Next reads it to nonce its inline scripts) and the
// policy is set on the response the browser receives.
function withNoncedCSP(
	req: NextRequest,
	baseRequestHeaders: Headers | undefined,
	nonce: string,
	csp: string,
): NextResponse {
	const requestHeaders = baseRequestHeaders ?? new Headers(req.headers);
	requestHeaders.set("x-nonce", nonce);
	requestHeaders.set("content-security-policy", csp);
	const response = NextResponse.next({ request: { headers: requestHeaders } });
	response.headers.set("Content-Security-Policy", csp);
	return response;
}

function isPublic(pathname: string): boolean {
	if (PUBLIC_PATHS.includes(pathname)) return true;
	// Next internals must always pass through.
	if (pathname.startsWith("/_next/")) return true;
	if (pathname.startsWith("/api/")) return true;
	// Same-origin backend proxy paths must reach the gateway/accounts service.
	// Page middleware is only a UX guard; redirecting these requests would turn
	// login into a 307 POST to /auth/login and bypass the backend's real auth
	// response semantics.
	if (pathname.startsWith("/v1/")) return true;
	if (pathname.startsWith("/saas.accounts.v1.")) return true;
	if (pathname === "/monitoring") return true;
	if (pathname.match(/\.(png|jpg|jpeg|gif|svg|ico|webp|avif|css|js|woff2?)$/))
		return true;
	return false;
}

export async function proxy(req: NextRequest) {
	const { pathname, search } = req.nextUrl;
	const gatewayHeaders = trustedGatewayRequestHeaders(
		req,
		resolveCodeflyGatewayContext(publicRequestOrigin(req)),
	);
	const nonce = mintNonce();

	const secretReturn =
		pathname === "/invitations/accept"
			? { query: "token", cookie: "invitation_return_token" }
			: pathname === "/waitlist/verify"
				? { query: "token", cookie: "waitlist_verification_token" }
				: null;
	if (secretReturn && req.nextUrl.searchParams.has(secretReturn.query)) {
		const token = req.nextUrl.searchParams.get(secretReturn.query) ?? "";
		const cleanURL = req.nextUrl.clone();
		cleanURL.searchParams.delete(secretReturn.query);
		const response = NextResponse.redirect(cleanURL);
		response.headers.set("Referrer-Policy", "no-referrer");
		if (token.length >= 32 && token.length <= 512) {
			response.cookies.set(secretReturn.cookie, token, {
				httpOnly: true,
				secure: process.env.NODE_ENV === "production",
				sameSite: "strict",
				path: pathname,
				maxAge: 15 * 60,
			});
		}
		return response;
	}

	// The session cookie set by AuthProvider on login. Its contents are not
	// validated here — if present we trust it and let the backend gateway do the
	// real validation. An invalid cookie will just cause every backend call to
	// 401, which is fine. It also decides the CSP: a document served to a signed-in
	// visitor can client-navigate into a solution, whatever its path — "/" is a
	// public path AND the signed-in home — so the policy follows the session, not
	// the route (see registeredSolutionOrigins).
	const session = req.cookies.get("codefly_session");

	if (isPublic(pathname)) {
		const csp = contentSecurityPolicyFromInputs(
			baselineCspInputs(),
			session ? await registeredSolutionOrigins(pathname) : [],
			nonce,
		);
		const response = withNoncedCSP(req, gatewayHeaders, nonce, csp);
		if (pathname === "/invitations/accept" || pathname === "/waitlist/verify") {
			response.headers.set("Referrer-Policy", "no-referrer");
		}
		return response;
	}

	if (!session) {
		const loginURL = req.nextUrl.clone();
		loginURL.pathname = "/auth/login";
		loginURL.searchParams.set("next", pathname + search);
		return NextResponse.redirect(loginURL);
	}

	const csp = contentSecurityPolicyFromInputs(
		baselineCspInputs(),
		await registeredSolutionOrigins(pathname),
		nonce,
	);
	return withNoncedCSP(req, gatewayHeaders, nonce, csp);
}

export const config = {
	// Match everything EXCEPT the Next.js internals that we already
	// bypass in isPublic. This narrower matcher avoids running the
	// proxy on every static asset.
	matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
