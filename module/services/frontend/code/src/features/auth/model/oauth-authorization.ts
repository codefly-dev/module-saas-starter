/**
 * The OAuth 2.1 authorization request, as this app handles it (issue #1003).
 *
 * The host is the only sign-in UI, so the standard authorization endpoint lives
 * here rather than in accounts: it has to show a login page and, for a client
 * the operator never declared or a request that names a resource, a consent
 * screen. Nothing here decides whether a request is allowed — accounts does,
 * before any sign-in UI renders, and again before it mints a code.
 *
 * Shared by the route handler at /oauth2/authorize (server) and the login page
 * (browser), which is why the parameter names and their parsing live in one
 * place: a parameter spelled differently on the two halves is a parameter the
 * person approves and the host does not bind.
 */

/** The OAuth parameters this host reads from an authorization request. */
export interface OAuthAuthorizationRequest {
	responseType: string;
	clientId: string;
	redirectUri: string;
	codeChallenge: string;
	codeChallengeMethod: string;
	state: string;
	scope: string;
	resource: string;
}

type QueryReader = { get(name: string): string | null };

/**
 * Reads an authorization request out of a query string. Returns null when the
 * three parameters without which there is no request at all are absent — a
 * plain visit to the login page, not a malformed authorization request.
 */
export function readOAuthAuthorizationRequest(
	query: QueryReader,
): OAuthAuthorizationRequest | null {
	const clientId = query.get("client_id");
	const redirectUri = query.get("redirect_uri");
	const codeChallenge = query.get("code_challenge");
	if (!clientId || !redirectUri || !codeChallenge) return null;
	return {
		responseType: query.get("response_type") ?? "code",
		clientId,
		redirectUri,
		codeChallenge,
		codeChallengeMethod: query.get("code_challenge_method") ?? "S256",
		state: query.get("state") ?? "",
		scope: query.get("scope") ?? "",
		resource: query.get("resource") ?? "",
	};
}

/** The wire shape accounts reads: the OAuth parameter names, unchanged. */
export function oauthWireFormat(request: OAuthAuthorizationRequest) {
	return {
		response_type: request.responseType,
		client_id: request.clientId,
		redirect_uri: request.redirectUri,
		code_challenge: request.codeChallenge,
		code_challenge_method: request.codeChallengeMethod,
		state: request.state,
		scope: request.scope,
		resource: request.resource,
	};
}

/** Renders the request back into a query string, for the login-page handoff. */
export function oauthQueryString(request: OAuthAuthorizationRequest): string {
	const query = new URLSearchParams();
	for (const [name, value] of Object.entries(oauthWireFormat(request))) {
		if (value) query.set(name, value);
	}
	return query.toString();
}

/** What accounts answers a validate call with. */
export interface OAuthAuthorizationResolution {
	clientName: string;
	clientUri?: string;
	clientSource: "registry" | "metadata_document";
	resource?: string;
	resourceName?: string;
	scope: string;
	requiresConsent: boolean;
}

/** What accounts answers a refusal with. */
export interface OAuthAuthorizationRefusal {
	error: string;
	errorDescription?: string;
	/**
	 * Whether the redirect URI was validated before the refusal, and so whether
	 * the error may be delivered to it. Sending a person to an unvalidated
	 * redirect URI is how an authorization endpoint becomes an open redirector,
	 * so this flag — not the presence of a redirect_uri parameter — is what
	 * decides.
	 */
	redirectable: boolean;
}

export function isOAuthRefusal(
	value: unknown,
): value is OAuthAuthorizationRefusal {
	return (
		typeof value === "object" &&
		value !== null &&
		typeof (value as { error?: unknown }).error === "string"
	);
}

/**
 * Builds the redirect back to the client. The redirect URI is the one the host
 * validated against the registry or the client's own metadata document, so it
 * is used verbatim; assembling one from anything the page still holds would be
 * assembling it from the caller's input again.
 */
export function oauthRedirect(
	redirectUri: string,
	parameters: Record<string, string | undefined>,
): string {
	const target = new URL(redirectUri);
	for (const [name, value] of Object.entries(parameters)) {
		if (value) target.searchParams.set(name, value);
	}
	return target.toString();
}
