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
		const response = NextResponse.next();
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

	return NextResponse.next();
}

export const config = {
	// Match everything EXCEPT the Next.js internals that we already
	// bypass in isPublic. This narrower matcher avoids running the
	// proxy on every static asset.
	matcher: ["/((?!_next/static|_next/image|favicon.ico).*)"],
};
