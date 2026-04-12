// Next.js middleware — runs on every request matching the matcher
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

import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

const PUBLIC_PATHS = [
  "/",
  "/auth/login",
  "/auth/callback",
  "/auth/logout",
  "/pricing",
  "/health",
  "/favicon.ico",
];

function isPublic(pathname: string): boolean {
  if (PUBLIC_PATHS.includes(pathname)) return true;
  // Next internals must always pass through.
  if (pathname.startsWith("/_next/")) return true;
  if (pathname.startsWith("/api/")) return true;
  if (pathname.match(/\.(png|jpg|jpeg|gif|svg|ico|webp|avif|css|js|woff2?)$/)) return true;
  return false;
}

export function middleware(req: NextRequest) {
  const { pathname, search } = req.nextUrl;

  if (isPublic(pathname)) {
    return NextResponse.next();
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
  // middleware on every static asset.
  matcher: [
    "/((?!_next/static|_next/image|favicon.ico).*)",
  ],
};
