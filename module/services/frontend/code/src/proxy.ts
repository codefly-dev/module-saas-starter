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

// The internal solution detail lookup, served by THIS server (see
// src/app/api/internal/solutions/route.ts). The public listing beside it
// carries only nav entries, so the manifest origins this derives a CSP from
// come from the token-gated route rather than from anything a browser can read.
const SOLUTION_LISTING_PATH = "/api/internal/solutions";
// The listing fetch is bounded so a wedged listener cannot stall a page.
const SOLUTION_LISTING_TIMEOUT_MS = 2_000;
// How long a listing result (including an empty one from a failed lookup) is
// reused. The registry changes only when a solution self-registers, and the nav
// itself polls the same listing every 10s (src/solutions/SolutionsMenu.tsx), so
// a few seconds of staleness is already the floor of how fast a new solution
// can surface. Caching here is what keeps a burst of concurrent documents from
// each opening its own loopback request against the server serving them.
const SOLUTION_LISTING_TTL_MS = 5_000;
// A CSP header this long is close to the point where a reverse proxy's response
// header buffer (nginx proxy_buffer_size defaults to 4k/8k) rejects the upstream
// response outright. The policy cannot be truncated — dropping origins would
// silently break the solutions they belong to — so this warns while the site is
// still serving, rather than letting the failure surface as an opaque 502.
const SOLUTION_CSP_WARN_BYTES = 8 * 1024;

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

// A browser enforces the CSP of the DOCUMENT that is executing. Every
// subresource, XHR, Connect RPC and RSC payload fetch runs under the policy of
// the document that issued it and never carries one of its own, so deriving the
// registered origins for a non-document request spends a loopback round trip to
// compute a header nothing will read. That is not merely wasteful: /v1/*,
// /saas.accounts.v1.*, /api/* and every client-side navigation are all matched
// by this proxy and all carry the session cookie, so keying the lookup on the
// cookie alone put a second request through this same server on the backend-API
// hot path — doubling authenticated request volume and letting a slow listing
// add its whole timeout to every API call, with the extra load feeding back into
// the listener being waited on.
//
// Sec-Fetch-Dest is the browser's own statement of what it will do with the
// response: "document" for a top-level navigation, "empty" for fetch/XHR
// (including Next's RSC payload requests). The Accept fallback covers clients
// that predate it. A caller that is neither (curl, a health probe, the loopback
// lookup below) enforces no CSP at all, so a self-only policy costs it nothing.
function isDocumentRequest(req: NextRequest): boolean {
	const dest = req.headers.get("sec-fetch-dest");
	if (dest !== null) {
		return dest === "document";
	}
	return (req.headers.get("accept") ?? "").includes("text/html");
}

let listingCache: { origins: string[]; expiresAt: number } | null = null;
let listingInFlight: Promise<string[]> | null = null;
// Dedup key for the last reported listing failure. The failure modes here are
// sticky, not transient — a PORT that does not match the real listener, a
// listing fronted differently in a multi-replica deploy, a 5xx window — so
// logging per request would emit one identical line per document served for as
// long as the misconfiguration lasts. Report each distinct state once, and
// report the recovery, which is what an operator actually needs to see.
let lastListingFailure: string | null = null;

function reportListingFailure(
	key: string,
	message: string,
	err?: unknown,
): void {
	if (lastListingFailure === key) {
		return;
	}
	lastListingFailure = key;
	if (err === undefined) {
		console.error(message);
	} else {
		console.error(message, err);
	}
}

// The oversized-policy warning has its own dedup state: it is a property of the
// registered origin set, not of the listing's health, so it must not be cleared
// by a successful lookup. The byte count is stable for a given origin set (the
// nonce is fixed width), so keying on it re-reports only when the set changes.
let lastOversizedCspBytes = 0;

function reportOversizedCsp(bytes: number): void {
	if (lastOversizedCspBytes === bytes) {
		return;
	}
	lastOversizedCspBytes = bytes;
	console.error(
		`solution CSP: policy is ${bytes} bytes; a reverse proxy may reject the response header (nginx proxy_buffer_size defaults to 4k/8k). Serve solutions from fewer origins, or same-origin through the host.`,
	);
}

function reportListingRecovered(): void {
	if (lastListingFailure === null) {
		return;
	}
	lastListingFailure = null;
	console.info("solution CSP: registry listing recovered");
}

/** Origin of an absolute http(s) URL, or null for anything unparseable. */
function manifestOrigin(value: unknown): string | null {
	if (typeof value !== "string") {
		return null;
	}
	try {
		return new URL(value).origin;
	} catch {
		return null;
	}
}

// Fetch and parse the listing. Total by construction: every path returns an
// array, so a malformed payload degrades to a self-only policy (logged) instead
// of throwing out of the proxy. That matters because this now runs for every
// document — an unguarded parse here would turn one bad registry entry into a
// 500 on every page rather than on one solution page.
async function loadRegisteredSolutionOrigins(
	pathname: string,
	internalToken: string,
): Promise<string[]> {
	// Mirror Next's standalone server: parseInt(PORT, 10) || 3000, so an unset,
	// empty, or non-numeric PORT resolves to the same port the server bound.
	const port = Number.parseInt(process.env.PORT ?? "", 10) || 3000;
	const listingUrl = new URL(SOLUTION_LISTING_PATH, `http://127.0.0.1:${port}`);
	let payload: unknown;
	try {
		const listing = await fetch(listingUrl, {
			headers: {
				accept: "application/json",
				[INTERNAL_TOKEN_HEADER]: internalToken,
			},
			signal: AbortSignal.timeout(SOLUTION_LISTING_TIMEOUT_MS),
		});
		if (!listing.ok) {
			reportListingFailure(
				`status:${listing.status}`,
				`solution CSP: registry listing responded ${listing.status} path=${pathname}`,
			);
			return [];
		}
		payload = await listing.json();
	} catch (err) {
		reportListingFailure(
			"unreachable",
			`solution CSP: registry listing unavailable path=${pathname}`,
			err,
		);
		return [];
	}
	const solutions =
		typeof payload === "object" && payload !== null
			? (payload as { solutions?: unknown }).solutions
			: undefined;
	if (!Array.isArray(solutions)) {
		reportListingFailure(
			"malformed",
			`solution CSP: registry listing returned no solutions array path=${pathname}`,
		);
		return [];
	}
	// manifestUrl is validated as an absolute http(s) URL at registration, but
	// this is the only reader of a payload that crosses a process boundary, so it
	// re-checks rather than trusting that invariant on every document. Two
	// solutions served from one origin collapse to a single source expression.
	const origins = new Set<string>();
	for (const solution of solutions) {
		const frontend =
			typeof solution === "object" && solution !== null
				? (solution as { frontend?: { manifestUrl?: unknown } }).frontend
				: undefined;
		const origin = manifestOrigin(frontend?.manifestUrl);
		if (origin !== null) {
			origins.add(origin);
		}
	}
	reportListingRecovered();
	return [...origins];
}

// A solution's Module Federation remote registers at RUNTIME (see
// src/solutions/registry.ts), so the build-time CSP in next.config cannot know
// its origin. Next runs this proxy in a context whose module singletons and
// globals are NOT shared with route handlers or pages (see the Next "proxy"
// docs: "you should not attempt relying on shared modules or globals"), so it
// cannot read the in-process registry the register endpoint and solution page
// share. Instead it asks the host over the local internal detail lookup — which
// does run in that shared context — and admits every registered remote's
// origin, letting a freshly registered cross-origin remote load with no rebuild
// and no FRONTEND_SOLUTION_ORIGINS entry.
//
// Every document gets the full registered set, not only /s/:id. A CSP is
// document-scoped, and the sidebar reaches a solution through client-side
// navigation (next/link), which swaps the RSC payload but keeps the policy of
// the document the user started in. Widening only /s/:id therefore left the
// manifest fetch blocked by that starting document's self-only connect-src
// (Module Federation RUNTIME-003) until a hard reload landed a fresh document
// on /s/:id (#545).
//
// The set is NOT gated on the session cookie, and that is load-bearing rather
// than lax. The cookie is written client-side by AuthProvider after the login
// response lands (src/lib/auth.tsx), so the login DOCUMENT was always served
// without it; fixture login and header-injected login then reach the app with
// router.push (src/features/auth/ui/login-page.tsx), a soft navigation that
// keeps that cookieless document's policy. Gating on the cookie would leave
// exactly those two flows self-only — the original bug, one document further
// along. Nor would it protect anything: the origins it admits are already
// visible to the browser as the `script-src` and `connect-src` it enforces, and
// they reach this server over a token-gated internal lookup that the browser
// cannot read (app/api/internal/solutions).
//
// The listing is fetched over loopback at the port THIS server binds — read
// from PORT with the same fallback Next's standalone server uses, so it always
// matches the actual listener. It must NOT be derived from the request origin
// (the client-controlled Host header): routing a server-side fetch through Host
// is an SSRF sink, and behind a TLS-terminating ingress the "self" origin is the
// public hostname, so the request would egress back out instead of staying
// local. On any failure the CSP falls back to self-only and the cause is logged
// rather than swallowed, since a silent fallback is indistinguishable from the
// very bug this fixes.
async function registeredSolutionOrigins(
	req: NextRequest,
	pathname: string,
	internalToken: string | undefined,
): Promise<string[]> {
	if (!isDocumentRequest(req)) {
		return [];
	}
	// No cluster-internal token means the detail lookup would 401 — and means
	// registration itself is failing closed, so there is no registered remote to
	// admit. Self-only is the correct policy here, not a degradation, but it is
	// still reported: silently serving it would hide a missing secret behind a
	// policy that merely looks conservative.
	if (!internalToken) {
		reportListingFailure(
			"no-internal-token",
			`solution CSP: no cluster-internal token; solution detail lookup unavailable path=${pathname}`,
		);
		return [];
	}
	// The lookup is served by this same server, so the loopback fetch runs back
	// through this proxy. It carries `accept: application/json` and no
	// Sec-Fetch-Dest, so the document gate above already stops it from fetching
	// again — but name the path outright so the absence of recursion is a stated
	// invariant and not an accident of the loader's request headers.
	if (pathname === SOLUTION_LISTING_PATH) {
		return [];
	}
	const cached = listingCache;
	if (cached !== null && Date.now() < cached.expiresAt) {
		return cached.origins;
	}
	if (listingInFlight === null) {
		listingInFlight = loadRegisteredSolutionOrigins(pathname, internalToken)
			.catch((err) => {
				// loadRegisteredSolutionOrigins is total, so this is unreachable
				// short of a defect in it. Report and degrade rather than letting a
				// throw here 500 every document the server is serving.
				console.error("solution CSP: registry lookup failed", err);
				return [] as string[];
			})
			.then((origins) => {
				listingCache = {
					origins,
					expiresAt: Date.now() + SOLUTION_LISTING_TTL_MS,
				};
				return origins;
			})
			.finally(() => {
				listingInFlight = null;
			});
	}
	return listingInFlight;
}

/**
 * The policy for one response: the build-time snapshot plus, for a document,
 * every registered solution origin. `baselineCspInputs` is read first so a
 * build that failed to inline the snapshot fails before any network work.
 */
async function contentSecurityPolicyFor(
	req: NextRequest,
	pathname: string,
	nonce: string,
	internalToken: string | undefined,
): Promise<string> {
	const inputs = baselineCspInputs();
	const csp = contentSecurityPolicyFromInputs(
		inputs,
		await registeredSolutionOrigins(req, pathname, internalToken),
		nonce,
	);
	if (csp.length > SOLUTION_CSP_WARN_BYTES) {
		reportOversizedCsp(csp.length);
	}
	return csp;
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
	const gatewayContext = resolveCodeflyGatewayContext(publicRequestOrigin(req));
	const gatewayHeaders = trustedGatewayRequestHeaders(req, gatewayContext);
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
		const csp = await contentSecurityPolicyFor(
			req,
			pathname,
			nonce,
			gatewayContext?.internalToken,
		);
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
	// It gates the login redirect only: the CSP deliberately does not
	// follow it (see registeredSolutionOrigins).
	const session = req.cookies.get("codefly_session");
	if (!session) {
		const loginURL = req.nextUrl.clone();
		loginURL.pathname = "/auth/login";
		loginURL.searchParams.set("next", pathname + search);
		return NextResponse.redirect(loginURL);
	}

	const csp = await contentSecurityPolicyFor(
		req,
		pathname,
		nonce,
		gatewayContext?.internalToken,
	);
	return withNoncedCSP(req, gatewayHeaders, nonce, csp);
}

export const config = {
	// Match everything EXCEPT the Next.js internals that we already
	// bypass in isPublic. This narrower matcher avoids running the
	// proxy on every static asset.
	matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
