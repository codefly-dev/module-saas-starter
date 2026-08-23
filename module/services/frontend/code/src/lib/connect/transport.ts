import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { rateLimitInterceptor } from "./rate-limit-tracker";
import { getToken, refreshToken } from "./token-store";

/**
 * Connect transport for the API backend, going through the auth-sidecar
 * gateway. All Connect RPC calls go through this single entry point.
 *
 * The auth interceptor automatically injects the Bearer token from the
 * token store on every request. The AuthProvider keeps the store in sync.
 *
 * Access tokens are short-lived (minutes), so an in-page session outlives
 * them by design: on Unauthenticated the interceptor exchanges the httpOnly
 * refresh cookie for a fresh token (single-flight across concurrent RPCs)
 * and retries the call once. Exported for tests.
 */
export const authInterceptor: Interceptor = (next) => async (req) => {
	const token = getToken();
	if (token) {
		req.header.set("Authorization", `Bearer ${token}`);
	}
	try {
		return await next(req);
	} catch (error) {
		if (!token || ConnectError.from(error).code !== Code.Unauthenticated) {
			throw error;
		}
		const fresh = await refreshToken();
		if (!fresh) throw error;
		req.header.set("Authorization", `Bearer ${fresh}`);
		return next(req);
	}
};

export const apiTransport = createConnectTransport({
	baseUrl: "/",
	// Order matters: auth runs first (sets Authorization), rate-limit
	// tracker runs second so it sees the response headers from the
	// auth'd call. Both are unary-only — no streaming wrapping needed.
	interceptors: [authInterceptor, rateLimitInterceptor],
});
