/**
 * A registered first-party client (an add-in, a CLI, a mobile app) sends the
 * person here rather than to an identity provider: the host is the only
 * sign-in UI. The client puts its authorization request in the login page's
 * query string, the host signs the person in however it is configured, and the
 * browser is handed back to the client with a one-time authorization code.
 *
 * Nothing here decides whether a request is allowed. The host does, before any
 * sign-in UI is rendered — see validateClientAuthorization.
 */
export interface ClientAuthorizationRequest {
	clientId: string;
	redirectUri: string;
	state: string;
	codeChallenge: string;
	codeChallengeMethod: string;
}

const PENDING_KEY = "client_authorization_request";

type QueryReader = { get(name: string): string | null };

/** Reads a client's authorization request out of the login page's URL. */
export function readClientAuthorizationRequest(
	query: QueryReader,
): ClientAuthorizationRequest | null {
	const clientId = query.get("client_id");
	const redirectUri = query.get("redirect_uri");
	const codeChallenge = query.get("code_challenge");
	if (!clientId || !redirectUri || !codeChallenge) return null;
	return {
		clientId,
		redirectUri,
		state: query.get("state") ?? "",
		codeChallenge,
		codeChallengeMethod: query.get("code_challenge_method") ?? "S256",
	};
}

function wireFormat(request: ClientAuthorizationRequest) {
	return {
		client_id: request.clientId,
		redirect_uri: request.redirectUri,
		code_challenge: request.codeChallenge,
		code_challenge_method: request.codeChallengeMethod,
	};
}

/**
 * Asks the host whether this request may proceed. A client id nobody
 * registered, or a redirect URI that client did not register, is refused here —
 * while the browser is still on the host and before the person has been shown
 * anywhere to type a credential. Resolves to the client's display name.
 */
export async function validateClientAuthorization(
	request: ClientAuthorizationRequest,
): Promise<string> {
	const response = await fetch("/v1/auth/clients/validate", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ authorization: wireFormat(request) }),
	});
	if (!response.ok) {
		throw new Error(
			"This application is not registered to sign in here, or asked to be returned to an address it has not registered.",
		);
	}
	const data = await response.json();
	return typeof data.clientName === "string" && data.clientName.length > 0
		? data.clientName
		: request.clientId;
}

/**
 * Holds the validated request across the sign-in itself, which may bounce
 * through an identity provider and back. sessionStorage, not a cookie or the
 * URL: it dies with the tab and never reaches a server.
 */
export function rememberClientAuthorization(
	request: ClientAuthorizationRequest,
): void {
	sessionStorage.setItem(PENDING_KEY, JSON.stringify(request));
}

export function forgetClientAuthorization(): void {
	sessionStorage.removeItem(PENDING_KEY);
}

/** Returns the pending request without consuming it. */
export function pendingClientAuthorization(): ClientAuthorizationRequest | null {
	const raw = sessionStorage.getItem(PENDING_KEY);
	if (!raw) return null;
	try {
		const parsed = JSON.parse(raw) as ClientAuthorizationRequest;
		return parsed.clientId && parsed.redirectUri && parsed.codeChallenge
			? parsed
			: null;
	} catch {
		return null;
	}
}

/** Returns the pending request and clears it, so one sign-in yields one code. */
export function takeClientAuthorization(): ClientAuthorizationRequest | null {
	const pending = pendingClientAuthorization();
	sessionStorage.removeItem(PENDING_KEY);
	return pending;
}

/**
 * Completes a pending handoff, if there is one, and reports whether it took the
 * browser away. Called wherever a sign-in has just produced a session — the
 * host's own session, which is what authorizes the code the client receives.
 */
export async function completePendingClientAuthorization(
	accessToken: string | null,
): Promise<boolean> {
	// Read without consuming. A request dropped before the host has actually
	// issued a code cannot be retried by anything — the client is left waiting
	// on a redirect that will never come — so it is cleared only once the code
	// is in hand.
	const request = pendingClientAuthorization();
	if (!request) return false;
	const response = await fetch("/v1/auth/clients/authorize", {
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
		},
		credentials: "include",
		body: JSON.stringify({ authorization: wireFormat(request) }),
	});
	if (!response.ok) {
		throw new Error(
			"Sign-in succeeded but the application could not be authorized.",
		);
	}
	const data = await response.json();
	if (typeof data.code !== "string" || data.code.length === 0) {
		throw new Error(
			"Sign-in succeeded but the application could not be authorized.",
		);
	}
	// The redirect target is the one the host validated against its registry, so
	// it is used verbatim; a URL assembled from anything the page still holds
	// would be assembling it from the caller's own input again.
	forgetClientAuthorization();
	const target = new URL(request.redirectUri);
	target.searchParams.set("code", data.code);
	if (request.state) target.searchParams.set("state", request.state);
	window.location.replace(target.toString());
	return true;
}
