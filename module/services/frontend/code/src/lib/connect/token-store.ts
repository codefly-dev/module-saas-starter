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
