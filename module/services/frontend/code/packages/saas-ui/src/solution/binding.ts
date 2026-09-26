/**
 * What the host hands every solution remote it mounts — the part of
 * `SolutionPageProps` a remote's own code reaches its backend with. The host's
 * `SolutionPageProps` extends this interface, so the remote and the host read
 * one definition rather than two copies that drift.
 *
 * Widening this is a minor of the host↔remote contract; making a field
 * required that a remote already receives optionally, or removing one, is a
 * major. Never add a required field: an older host does not inject it, and the
 * remote throws inside the host's error boundary, blanking the page.
 */
export interface SolutionBinding {
	solutionId: string;
	/**
	 * Same-origin base for ALL of a remote's backend calls — its own service and
	 * the host's platform services alike. ONE base covers both because the host
	 * proxy routes on the path, not on the base: a Connect procedure shaped
	 * `saas.<pkg>.v1.<Service>/<Method>` goes to the API gateway's root, exactly
	 * where a host page's own call lands, and everything else goes to this
	 * solution's registered upstream.
	 *
	 * So a kit component the host hands a remote — `<DatasourcesPanel gateway>`
	 * calls the host's `saas.accounts.v1.DatasourceService` — works over this
	 * base unchanged. Do NOT add a second "host" base: it would be the same
	 * destination reached a second way.
	 */
	apiBase: string;
	/** Host-owned access-token getter — the remote never touches the token store. */
	getAccessToken: () => string | null;
	/**
	 * Host-owned refresh: exchanges the httpOnly session for a fresh access token
	 * (single-flight) and resolves to it, or null if the session is gone.
	 */
	refreshAccessToken: () => Promise<string | null>;
	/**
	 * Host-owned authed fetch: stamps the bearer token, and on a 401 exchanges
	 * the session for a fresh token (single-flight) and retries the request once.
	 * A solution making raw REST calls to its own backend uses this — through
	 * `solutionFetch` — rather than hand-rolling the bearer, so every solution
	 * gets the portal's refresh-then-retry recovery.
	 */
	authedFetch: (
		input: RequestInfo | URL,
		init?: RequestInit,
	) => Promise<Response>;
}

/**
 * The part of the binding the helpers below need. Everything past `apiBase` is
 * optional so a remote mounted by an older host — or rendered in a test —
 * still works: without `authedFetch` a request goes out with `fetch` and the
 * bearer `getAccessToken` returns.
 */
export type SolutionRequestBinding = Pick<SolutionBinding, "apiBase"> &
	Partial<Pick<SolutionBinding, "getAccessToken" | "authedFetch">>;
