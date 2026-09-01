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
 * Fetch that carries the host's bearer token and recovers from a lapsed access
 * token exactly as the Connect transport's interceptor does: on a 401 it
 * exchanges the session for a fresh token (single-flight, shared with every
 * other caller) and retries the request once. A null refresh means the session
 * is truly gone — the registered refresh handler has already torn down local
 * state and redirected to login — so the original 401 is returned to the
 * caller. A solution page doing raw REST calls uses this instead of
 * hand-rolling fetch + getToken, so it gets the same mid-session recovery.
 */
export async function authedFetch(
	input: RequestInfo | URL,
	init: RequestInit = {},
): Promise<Response> {
	const token = getToken();
	const res = await fetch(input, withBearer(init, token));
	if (res.status !== 401 || !token) return res;
	const fresh = await refreshToken();
	if (!fresh) return res;
	return fetch(input, withBearer(init, fresh));
}

function withBearer(init: RequestInit, token: string | null): RequestInit {
	if (!token) return init;
	const headers = new Headers(init.headers);
	headers.set("Authorization", `Bearer ${token}`);
	return { ...init, headers };
}
