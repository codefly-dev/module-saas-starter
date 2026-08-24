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
import type { SolutionManifest } from "@/solutions/registry";
import { contentSecurityPolicyFromInputs } from "../server/security-headers.mjs";

const PRODUCT_API_PREFIXES = ["/v1/", "/saas.accounts.v1."] as const;
const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";
const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

function isProductAPI(pathname: string): boolean {
	return PRODUCT_API_PREFIXES.some((prefix) => pathname.startsWith(prefix));
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

const SOLUTION_PAGE = /^\/s\/([^/]+)/;

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

function safeDecode(segment: string): string | null {
	try {
		return decodeURIComponent(segment);
	} catch {
		return null;
	}
}

// A solution's Module Federation remote registers at RUNTIME (see
// src/solutions/registry.ts), so the build-time CSP in next.config — which
// excludes /s/:id precisely for this reason — cannot know its origin. This
// proxy runs on the Node.js runtime, so it shares the process global the
// registry anchors on; for a solution page it derives the remote's origin from
// the registration and returns a CSP that allows it, letting a freshly
// registered cross-origin remote load with no rebuild and no
// FRONTEND_SOLUTION_ORIGINS entry. Non-solution pages keep their build-time CSP.
// The registry type is imported for typing only (erased at compile), so this
// does not pull the server-only registry module into the proxy bundle.
function solutionContentSecurityPolicy(
	pathname: string,
	nonce: string,
): string | null {
	const match = SOLUTION_PAGE.exec(pathname);
	if (!match) {
		return null;
	}
	const id = safeDecode(match[1]);
	const registry = (
		globalThis as typeof globalThis & {
			__solutionRegistry?: Map<string, SolutionManifest>;
		}
	).__solutionRegistry;
	const solution = id === null ? undefined : registry?.get(id);
	// manifestUrl is validated as an absolute http(s) URL at registration.
	const origins = solution
		? [new URL(solution.frontend.manifestUrl).origin]
		: [];
	return contentSecurityPolicyFromInputs(baselineCspInputs(), origins, nonce);
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

export function proxy(req: NextRequest) {
	const { pathname, search } = req.nextUrl;
	const gatewayHeaders = trustedGatewayRequestHeaders(
		req,
		resolveCodeflyGatewayContext(req.nextUrl.origin),
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

	if (isPublic(pathname)) {
		const csp = contentSecurityPolicyFromInputs(baselineCspInputs(), [], nonce);
		const response = withNoncedCSP(req, gatewayHeaders, nonce, csp);
		if (pathname === "/invitations/accept" || pathname === "/waitlist/verify") {
			response.headers.set("Referrer-Policy", "no-referrer");
		}
		return response;
	}

	// Check the session cookie set by AuthProvider on login. The
	// cookie contents are not validated here — if present we trust it
	// and let the backend gateway do the real validation. An invalid
	// cookie will just cause every backend call to 401, which is fine.
	const session = req.cookies.get("codefly_session");
	if (!session) {
		const loginURL = req.nextUrl.clone();
		loginURL.pathname = "/auth/login";
		loginURL.searchParams.set("next", pathname + search);
		return NextResponse.redirect(loginURL);
	}

	const csp =
		solutionContentSecurityPolicy(pathname, nonce) ??
		contentSecurityPolicyFromInputs(baselineCspInputs(), [], nonce);
	return withNoncedCSP(req, gatewayHeaders, nonce, csp);
}

export const config = {
	// Match everything EXCEPT the Next.js internals that we already
	// bypass in isPublic. This narrower matcher avoids running the
	// proxy on every static asset.
	matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
