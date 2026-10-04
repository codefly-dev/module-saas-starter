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
 * Every parameter the standard authorization endpoint reads. Each is a
 * singleton: RFC 6749 §3.1 says a request parameter MUST NOT appear more than
 * once, and `URLSearchParams.get` returns the first and discards the rest —
 * which would make the host silently pick one of two values a caller sent.
 */
const OAUTH_SINGLETON_PARAMETERS = [
	"response_type",
	"client_id",
	"redirect_uri",
	"code_challenge",
	"code_challenge_method",
	"state",
	"scope",
	"resource",
] as const;

/**
 * Reads an authorization request out of a query string, repairing nothing.
 *
 * Returns null when the three parameters without which there is no request at
 * all are absent — a plain visit to the login page rather than a malformed
 * authorization request. Everything else is carried EXACTLY as sent, including
 * absent `response_type` and `code_challenge_method`: those used to be
 * defaulted to `code` and `S256` here, which meant the host was the only reason
 * a non-conforming request worked, and a client that believed it was sending
 * `plain` would have been silently upgraded. The host refuses them instead, and
 * tells the client which parameter it owes.
 */
export function readOAuthAuthorizationRequest(
	query: QueryReader,
): OAuthAuthorizationRequest | null {
	const clientId = query.get("client_id");
	const redirectUri = query.get("redirect_uri");
	const codeChallenge = query.get("code_challenge");
	if (!clientId || !redirectUri || !codeChallenge) return null;
	return {
		responseType: query.get("response_type") ?? "",
		clientId,
		redirectUri,
		codeChallenge,
		codeChallengeMethod: query.get("code_challenge_method") ?? "",
		state: query.get("state") ?? "",
		scope: query.get("scope") ?? "",
		resource: query.get("resource") ?? "",
	};
}

/**
 * Reads a request that arrived at the LOGIN PAGE rather than at the standard
 * authorization endpoint, applying the two defaults that path has always
 * applied.
 *
 * The first registered client (and anything built against the shape the
 * registered-clients decision documents) opens
 * `/auth/login?client_id=…&redirect_uri=…&state=…&code_challenge=…` directly.
 * It sends no `response_type`, and `code_challenge_method` has always been
 * assumed. Making the login page strict would refuse a shipped client for a
 * parameter the contract it was written against never had.
 *
 * Both defaults are safe here precisely because they are the only values this
 * host serves: `code` is the only response type and S256 the only challenge
 * method, so defaulting them cannot admit anything a strict read would have
 * refused for a different reason. What the standard endpoint must not do — and
 * no longer does — is repair a request from a client that DOES speak the
 * specification, because there the omission is the client's own bug and
 * silently fixing it hides a `plain` challenge the client thought it sent.
 */
export function readLegacyLoginAuthorizationRequest(
	query: QueryReader,
): OAuthAuthorizationRequest | null {
	const request = readOAuthAuthorizationRequest(query);
	if (!request) return null;
	return {
		...request,
		responseType: request.responseType || "code",
		codeChallengeMethod: request.codeChallengeMethod || "S256",
	};
}

/**
 * The first parameter given more than once, or null. The host refuses such a
 * request rather than choosing between the values for the caller.
 */
export function duplicatedOAuthParameter(
	query: URLSearchParams,
): string | null {
	for (const name of OAUTH_SINGLETON_PARAMETERS) {
		if (query.getAll(name).length > 1) return name;
	}
	return null;
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

/**
 * What accounts answers a validate call with.
 *
 * This is a DECODED value, never a cast one. The host serialises OAuth-style
 * snake_case (`requires_consent`, `client_name`); casting that JSON to a
 * camelCase interface compiles, type-checks, and silently yields `undefined`
 * for every field — which for `requiresConsent` means falsy, which means the
 * consent screen is skipped and a code is issued for a client the person never
 * approved. That is exactly what happened, and no unit test on either side of
 * the seam could see it, because each mocked the other's shape. Decode at the
 * boundary and validate; see `parseOAuthResolution`.
 */
export interface OAuthAuthorizationResolution {
	/** Untrusted presentation, from the client's own document. */
	clientName: string;
	/**
	 * The origin the host VERIFIED — derived from the client_id URL it fetched
	 * the document from, never from anything the document claims. Always shown.
	 */
	clientOrigin: string;
	clientSource: "registry" | "metadata_document";
	resource?: string;
	resourceName?: string;
	scope: string;
	requiresConsent: boolean;
	/** The authorization server's own issuer, for the RFC 9207 `iss` echo. */
	issuer: string;
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
	/** Echoed on the error redirect (RFC 9207), when the host could name it. */
	issuer?: string;
}

function asString(value: unknown): string {
	return typeof value === "string" ? value : "";
}

/**
 * Decodes a validate response. Returns null for anything that is not a
 * well-formed resolution, so a caller FAILS CLOSED rather than reading a
 * missing `requires_consent` as "no consent needed".
 */
export function parseOAuthResolution(
	body: unknown,
): OAuthAuthorizationResolution | null {
	if (typeof body !== "object" || body === null) return null;
	const wire = body as Record<string, unknown>;
	// requires_consent decides whether a person is asked before a credential is
	// issued, so it must be present and boolean. Absent or any other type is a
	// response this code does not understand, and the safe reading of a response
	// it does not understand is "do not issue anything".
	if (typeof wire.requires_consent !== "boolean") return null;
	const source = asString(wire.client_source);
	if (source !== "registry" && source !== "metadata_document") return null;
	const clientOrigin = asString(wire.client_origin);
	if (clientOrigin === "") return null;
	return {
		clientName: asString(wire.client_name),
		clientOrigin,
		clientSource: source,
		resource: asString(wire.resource) || undefined,
		resourceName: asString(wire.resource_name) || undefined,
		scope: asString(wire.scope),
		requiresConsent: wire.requires_consent,
		issuer: asString(wire.issuer),
	};
}

/** Decodes an RFC 6749 §5.2 error object. */
export function parseOAuthRefusal(
	body: unknown,
): OAuthAuthorizationRefusal | null {
	if (typeof body !== "object" || body === null) return null;
	const wire = body as Record<string, unknown>;
	if (typeof wire.error !== "string" || wire.error === "") return null;
	return {
		error: wire.error,
		errorDescription: asString(wire.error_description) || undefined,
		redirectable: wire.redirectable === true,
		issuer: asString(wire.issuer) || undefined,
	};
}

/**
 * True when a body is a decodable OAuth error object. Kept as a predicate for
 * call sites that only need to branch; anything reading its FIELDS must use
 * parseOAuthRefusal, so a mistyped field cannot read as absent.
 */
export function isOAuthRefusal(value: unknown): boolean {
	return parseOAuthRefusal(value) !== null;
}

/** What accounts answers a grant call with. */
export interface OAuthAuthorizationGrant {
	code: string;
	issuer: string;
}

export function parseOAuthGrant(body: unknown): OAuthAuthorizationGrant | null {
	if (typeof body !== "object" || body === null) return null;
	const wire = body as Record<string, unknown>;
	const code = asString(wire.code);
	if (code === "") return null;
	return { code, issuer: asString(wire.issuer) };
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
