/**
 * Module-level token store that bridges React auth context and the
 * Connect transport interceptor. The AuthProvider writes the token here
 * on login/refresh; the interceptor reads it on every RPC call.
 */

let currentToken: string | null = null;

export function setToken(token: string | null) {
	currentToken = token;
}

export function getToken(): string | null {
	return currentToken;
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

export function setRefreshHandler(
	handler: (() => Promise<string | null>) | null,
) {
	refreshHandler = handler;
}

export function refreshToken(): Promise<string | null> {
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
 * Single-flight coordinator for the ONE on-load refresh-cookie exchange the
 * AuthProvider fires when the app boots. The refresh token is single-use and
 * rotates on the server the instant the exchange is accepted, so the exchange
 * must be presented at most once: a second concurrent presentation of the same
 * cookie looks to the backend like refresh-token reuse and, under the strict
 * OWASP rotation policy, revokes the entire session family (every device).
 *
 * React StrictMode double-invokes effects in development, and a fast remount
 * can overlap two AuthProvider bootstraps, so the on-load effect cannot rely on
 * being called once. This collapses concurrent bootstraps onto a single
 * in-flight exchange; callers that arrive while one is running share its result
 * instead of presenting the cookie again. It is deliberately separate from
 * `inflightRefresh` (mid-session 401 recovery) so a bootstrap and a mid-session
 * refresh never dedupe against each other — they exchange the same cookie but
 * are driven by different lifecycles.
 */
let inflightBootstrap: Promise<unknown> | null = null;

export function bootstrapRefresh<T>(exchange: () => Promise<T>): Promise<T> {
	if (!inflightBootstrap) {
		inflightBootstrap = exchange().finally(() => {
			inflightBootstrap = null;
		});
	}
	return inflightBootstrap as Promise<T>;
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
	const fresh = await refreshToken();
	if (!fresh) return res;
	retry.headers.set("Authorization", `Bearer ${fresh}`);
	return fetch(retry);
}
