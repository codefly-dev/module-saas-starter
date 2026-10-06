import type { DeclaredSource } from "../datasources/declared-source.js";

/**
 * What the host hands every solution remote it mounts — what a remote's own
 * code reaches its backend with, plus the host's copy of what this solution
 * declared about itself. The host's `SolutionPageProps` extends this
 * interface, so the remote and the host read one definition rather than two
 * copies that drift.
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
	 * Host-owned change notification: calls `listener` whenever the value
	 * `getAccessToken` returns changes, and returns an unsubscribe.
	 *
	 * Optional because an older host injects none, but a host that can notify
	 * SHOULD, and the difference is not cosmetic. The getter stays stable while
	 * the token rotates underneath it, so without this the kit has no way to learn
	 * that a rotation happened and falls back to re-reading on a short interval —
	 * a timer per observer that never lets the page idle. Its presence is the
	 * kit's only evidence that the host will tell it, so supplying it is exactly
	 * what switches that polling off.
	 */
	subscribeToken?: (listener: () => void) => () => void;
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
	/**
	 * The sources this solution declared it is built on, as the host validated
	 * and stored them at registration (`sources:` in the registration
	 * manifest). The host hands the declaration straight back rather than
	 * making the remote restate it in code: one statement, validated once,
	 * where an operator reading the registration and the person reading the
	 * page see the same repository.
	 *
	 * A declaration is not a connection and not a permission. It says which
	 * repository the solution reads; whether the organization has connected it
	 * is answered by the host's own source list, which is what
	 * `<DeclaredSourceCard>` reads.
	 *
	 * Optional, and absent from an older host: a remote reading it must treat
	 * absence as "the host does not tell me", never as "this solution declares
	 * nothing".
	 */
	declaredSources?: DeclaredSource[];
}

/**
 * The credential half of a request binding: at least one of the two, either
 * alone. The host's `authedFetch` stamps the viewer's bearer itself, so
 * `{ apiBase, authedFetch }` is a complete, authenticated binding; a
 * `getAccessToken` alone sends its bearer over plain `fetch`, losing only the
 * refresh-then-retry recovery (a remote mounted by an older host, or a test).
 *
 * What cannot be expressed is a binding with NEITHER: it would send the request
 * anonymously and the host would answer a bare `HTTP 401` — indistinguishable,
 * to the remote and to the person reading it, from a session that simply
 * expired. A misconfigured binding must not disguise itself as an ordinary
 * sign-in prompt, so the type refuses it, and `requestBinding` refuses it at
 * run time for a caller the type cannot see (plain JavaScript, a cast).
 */
export type SolutionCredential =
	| {
			getAccessToken: SolutionBinding["getAccessToken"];
			authedFetch?: SolutionBinding["authedFetch"];
	  }
	| {
			getAccessToken?: SolutionBinding["getAccessToken"];
			authedFetch: SolutionBinding["authedFetch"];
	  };

/**
 * The part of the binding the helpers below need: where to send a request, and
 * the credential to send it with. `getAccessToken` is optional here — a
 * required prop is a breaking change to every remote written against the
 * binding — but the credential as a whole is not.
 */
export type SolutionRequestBinding = Pick<SolutionBinding, "apiBase"> &
	Partial<Pick<SolutionBinding, "subscribeToken">> &
	SolutionCredential;

/** Thrown for a binding that carries no credential at all. */
export class SolutionBindingError extends Error {
	constructor() {
		super(
			"solution binding has no credential: pass authedFetch or getAccessToken",
		);
		this.name = "SolutionBindingError";
	}
}

/**
 * Rebuilds a request binding from its parts — the form a hook holds after
 * destructuring for its dependency list — and refuses one with no credential
 * instead of letting it go out anonymously.
 */
export function requestBinding(
	apiBase: string,
	getAccessToken: SolutionBinding["getAccessToken"] | undefined,
	authedFetch: SolutionBinding["authedFetch"] | undefined,
): SolutionRequestBinding {
	if (authedFetch) return { apiBase, getAccessToken, authedFetch };
	if (getAccessToken) return { apiBase, getAccessToken };
	throw new SolutionBindingError();
}

/** The bearer a binding carries, refusing a binding that carries no credential. */
export function bindingBearer(binding: SolutionRequestBinding): string | null {
	if (!binding.getAccessToken && !binding.authedFetch)
		throw new SolutionBindingError();
	return binding.getAccessToken?.() ?? null;
}
