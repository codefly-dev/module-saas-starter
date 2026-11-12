/**
 * Module-level token store that bridges React auth context and the
 * Connect transport interceptor. The AuthProvider writes the token here
 * on login/refresh; the interceptor reads it on every RPC call.
 */

let currentToken: string | null = null;

export function setToken(token: string | null) {
	if (currentToken === token) return;
	currentToken = token;
	// Solution remotes keep a stable getter. Notify them synchronously when its
	// value changes so the prior viewer's data does not survive an auth switch.
	// The event carries no token or identity; consumers read their host getter.
	if (typeof window !== "undefined")
		window.dispatchEvent(new Event("codefly:auth-changed"));
}

export function getToken(): string | null {
	return currentToken;
}

/**
 * Call `listener` whenever the stored token changes; returns an unsubscribe.
 *
 * `getToken` is deliberately a stable function over a moving value, so a remote
 * holding it cannot see a rotation. Handing it this alongside the getter is what
 * lets the kit stop re-reading on a timer: the events below are the mechanism,
 * and this is the promise that they exist. `focus` and `storage` are here too
 * because a session established in another tab changes nothing in this tab's
 * in-memory store until something makes it look again.
 */
export function subscribeToken(listener: () => void): () => void {
	if (typeof window === "undefined") return () => {};
	for (const event of ["codefly:auth-changed", "focus", "storage"])
		window.addEventListener(event, listener);
	return () => {
		for (const event of ["codefly:auth-changed", "focus", "storage"])
			window.removeEventListener(event, listener);
	};
}

/**
 * Refresh coordination for the transport's 401 recovery. The AuthProvider
 * registers a handler that exchanges the httpOnly refresh cookie for a new
 * access token (and updates auth state). Concurrent callers share one
 * in-flight refresh — the refresh token rotates on use, so a second
 * concurrent exchange would burn the freshly issued token.
 *
 * This single-flight is NOT redundant with the exchange-level coalescing in
 * auth.tsx (`inflightExchange`): this one also collapses the handler's
 * teardown/redirect side-effects, so two concurrent 401s recover into one
 * logout+redirect instead of two. See the layered note in auth.tsx's
 * `exchangeRefreshCookie` — deleting either single-flight reopens a distinct
 * bug (double redirect here, self-inflicted reuse revocation there).
 */
let refreshHandler: (() => Promise<string | null>) | null = null;
let inflightRefresh: Promise<string | null> | null = null;

/**
 * Every bearer this page has installed as a NARROWED session: a role view or a
 * team cast. Such a session is access-only and has no refresh half, so the only
 * token a refresh can produce is the person's own, full session. A request that
 * was sent with a narrowed bearer and came back 401 must therefore never be
 * retried with what a refresh returns: that would execute, as the person, an
 * action they took as less than themselves.
 *
 * The decision is made per request, from the bearer THAT request carried
 * (`refreshToken(sentWith)`), never from "is the page narrowed right now". A
 * flag of the page's state is what the first repair resets, so a second 401
 * from the same narrowed bearer, landing after that repair settled, would read
 * the page as no longer narrowed and be replayed with the full token. A bearer
 * stays here for the life of the page, so a late 401 after the person returned
 * to their own session is refused the same way.
 */
const narrowedBearers = new Set<string>();
let narrowedSessionEndedHandler: ((bearer: string) => void) | null = null;

export function markNarrowed(token: string) {
	narrowedBearers.add(token);
}

export function isNarrowed(token: string | null): boolean {
	return token !== null && narrowedBearers.has(token);
}

/**
 * Told, with the bearer, when a request sent under a narrowed session came
 * back 401: that session is over (its credential expired or was revoked), and
 * the AuthProvider says so to the person instead of silently handing them back
 * their own session.
 */
export function setNarrowedSessionEndedHandler(
	handler: ((bearer: string) => void) | null,
) {
	narrowedSessionEndedHandler = handler;
}

export function setRefreshHandler(
	handler: (() => Promise<string | null>) | null,
) {
	refreshHandler = handler;
}

/**
 * `sentWith` is the bearer the failed request carried; it defaults to the
 * installed token for a caller that has nothing else. A narrowed bearer is
 * answered null before any exchange and outside the single-flight: it must not
 * share a full-session refresh another caller is waiting on either.
 */
export function refreshToken(
	sentWith: string | null = currentToken,
): Promise<string | null> {
	if (isNarrowed(sentWith)) {
		narrowedSessionEndedHandler?.(sentWith as string);
		return Promise.resolve(null);
	}
	// A request sent with the person's OWN token before they narrowed, whose
	// 401 lands while the narrowed session is installed: no exchange. The only
	// token one could produce is the person's own, and installing it would put
	// them back in their own session with nothing said; the request fails
	// instead, and the narrowed session stays exactly as they chose it.
	if (isNarrowed(currentToken)) return Promise.resolve(null);
	if (!refreshHandler) return Promise.resolve(null);
	if (!inflightRefresh) {
		inflightRefresh = refreshHandler()
			.catch(() => null)
			.finally(() => {
				inflightRefresh = null;
			});
	}
	return inflightRefresh;
}

/**
 * Fetch that carries the host's bearer token and recovers from a lapsed access
 * token exactly as the Connect transport's interceptor does: on a 401 it
 * exchanges the session for a fresh token (single-flight, shared with every
 * other caller) and retries the request once. A null refresh yields no retry —
 * either the session is gone (the registered handler has already torn down
 * local state and redirected to login) or the refresh endpoint was transiently
 * unavailable (the session is kept) — so the original 401 is returned to the
 * caller. A solution page doing raw REST calls uses this instead of
 * hand-rolling fetch + getToken, so it gets the same mid-session recovery.
 */
export async function authedFetch(
	input: RequestInfo | URL,
	init: RequestInit = {},
): Promise<Response> {
	const token = getToken();
	const request = new Request(input, init);
	if (token) request.headers.set("Authorization", `Bearer ${token}`);
	// Clone before the body is consumed so the retry can replay a one-shot body
	// (a ReadableStream, or a `Request` passed as `input`); reusing the sent
	// request would throw "body already used" on the second fetch.
	const retry = request.clone();
	const res = await fetch(request);
	if (res.status !== 401 || !token) return res;
	const fresh = await refreshToken(token);
	if (!fresh) return res;
	retry.headers.set("Authorization", `Bearer ${fresh}`);
	return fetch(retry);
}
