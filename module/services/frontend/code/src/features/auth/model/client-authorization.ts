/**
 * A public client (an add-in, a CLI, a mobile app, an MCP client) sends the
 * person here rather than to an identity provider: the host is the only sign-in
 * UI. The client puts its authorization request in the login page's query
 * string, the host signs the person in however it is configured, and the
 * browser is handed back to the client with a one-time authorization code.
 *
 * Nothing here decides whether a request is allowed. The host does, before any
 * sign-in UI is rendered (`validateClientAuthorization`) and again before it
 * mints a code (`/v1/oauth2/authorize/grant`).
 *
 * Both halves talk to the standard authorization-server endpoints (issue
 * #1003), which carry the two parameters the older `/v1/auth/clients/*` RPCs
 * did not: `resource` (RFC 8707), which binds the token's audience, and
 * `scope`. Those RPCs still exist for the first registered client, which calls
 * the token endpoint directly; this page does not use them, because a page that
 * could not carry a resource could not sign anyone in to an MCP client.
 */

import {
	type OAuthAuthorizationRequest,
	type OAuthAuthorizationResolution,
	oauthRedirect,
	oauthWireFormat,
	parseOAuthGrant,
	parseOAuthRefusal,
	parseOAuthResolution,
	readLegacyLoginAuthorizationRequest,
} from "./oauth-authorization";

export type ClientAuthorizationRequest = OAuthAuthorizationRequest;

/**
 * Where the pending request and the host's answer about it live across the
 * sign-in itself, which may bounce through an identity provider and back, and
 * across the navigation to the consent page. sessionStorage, not a cookie or
 * the URL: it dies with the tab and never reaches a server.
 *
 * Exported because the consent page reads the store through
 * `useSyncExternalStore` rather than through the accessors below — React's own
 * way to read an external store, which needs the key and a stable raw snapshot
 * rather than a freshly parsed object on every call.
 */
export const PENDING_REQUEST_KEY = "client_authorization_request";
export const PENDING_RESOLUTION_KEY = "client_authorization_resolution";

/**
 * Reads a client's authorization request out of the LOGIN PAGE's URL, with the
 * two defaults that path has always applied — see
 * readLegacyLoginAuthorizationRequest for why they stay here and not on the
 * standard `/oauth2/authorize` endpoint.
 */
export const readClientAuthorizationRequest =
	readLegacyLoginAuthorizationRequest;

/**
 * Asks the host whether this request may proceed. A client id nobody
 * registered, a client whose metadata document does not validate, or a redirect
 * URI that client did not register is refused here — while the browser is still
 * on the host and before the person has been shown anywhere to type a
 * credential. Resolves to what the host says about the client and the resource.
 */
export async function validateClientAuthorization(
	request: ClientAuthorizationRequest,
): Promise<OAuthAuthorizationResolution> {
	const response = await fetch("/v1/oauth2/authorize/validate", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(oauthWireFormat(request)),
	});
	const body = await response.json().catch(() => null);
	if (!response.ok) {
		throw new Error(
			parseOAuthRefusal(body)?.errorDescription ??
				"This application is not registered to sign in here, or asked to be returned to an address it has not registered.",
		);
	}
	// DECODED, not cast. The host serialises OAuth snake_case; casting that JSON
	// to this camelCase interface compiled, type-checked, and yielded `undefined`
	// for every field — including `requiresConsent`, which is falsy, which meant
	// consent was skipped and a code issued for a client nobody approved. A
	// response this cannot decode is one whose meaning is unknown, and the only
	// safe reading of that is to refuse.
	const resolved = parseOAuthResolution(body);
	if (!resolved) {
		throw new Error("This application cannot sign in here.");
	}
	return resolved;
}

/**
 * Holds the validated request across the sign-in itself, which may bounce
 * through an identity provider and back, and across the navigation to the
 * consent page. sessionStorage, not a cookie or the URL: it dies with the tab
 * and never reaches a server.
 */
export function rememberClientAuthorization(
	request: ClientAuthorizationRequest,
	resolution?: OAuthAuthorizationResolution,
): void {
	sessionStorage.setItem(PENDING_REQUEST_KEY, JSON.stringify(request));
	if (resolution) {
		sessionStorage.setItem(PENDING_RESOLUTION_KEY, JSON.stringify(resolution));
	}
}

export function forgetClientAuthorization(): void {
	sessionStorage.removeItem(PENDING_REQUEST_KEY);
	sessionStorage.removeItem(PENDING_RESOLUTION_KEY);
}

/**
 * Parses a stored request. A half-formed one is no request: without all three
 * of client, redirect and challenge there is nothing the host would accept.
 * Separated from the read so the consent page can parse a snapshot React gave
 * it rather than touching the store during render.
 */
export function parsePendingClientAuthorization(
	raw: string | null,
): ClientAuthorizationRequest | null {
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

/**
 * What the host said about the pending request — the client's validated name
 * and origin, the resource, and whether the person must approve it. The consent
 * page renders these facts and nothing it derived itself.
 */
export function parsePendingClientResolution(
	raw: string | null,
): OAuthAuthorizationResolution | null {
	if (!raw) return null;
	let parsed: unknown;
	try {
		parsed = JSON.parse(raw);
	} catch {
		return null;
	}
	// Validated, not cast — the same discipline as the wire decoder, for the
	// same reason. This value has been outside this code's hands: it crossed a
	// sign-in, possibly a provider round trip, and a navigation, in storage the
	// page's own origin can write. A cast here would let a stored
	// `requiresConsent: false` skip the consent screen, which is the first
	// defect over again with a different carrier.
	if (typeof parsed !== "object" || parsed === null) return null;
	const stored = parsed as Record<string, unknown>;
	if (typeof stored.requiresConsent !== "boolean") return null;
	if (
		stored.clientSource !== "registry" &&
		stored.clientSource !== "metadata_document"
	) {
		return null;
	}
	if (typeof stored.clientOrigin !== "string" || stored.clientOrigin === "") {
		return null;
	}
	return {
		clientName: typeof stored.clientName === "string" ? stored.clientName : "",
		clientOrigin: stored.clientOrigin,
		clientSource: stored.clientSource,
		resource: typeof stored.resource === "string" ? stored.resource : undefined,
		resourceName:
			typeof stored.resourceName === "string" ? stored.resourceName : undefined,
		scope: typeof stored.scope === "string" ? stored.scope : "",
		requiresConsent: stored.requiresConsent,
		issuer: typeof stored.issuer === "string" ? stored.issuer : "",
	};
}

/** Returns the pending request without consuming it. */
export function pendingClientAuthorization(): ClientAuthorizationRequest | null {
	return parsePendingClientAuthorization(
		sessionStorage.getItem(PENDING_REQUEST_KEY),
	);
}

export function pendingClientResolution(): OAuthAuthorizationResolution | null {
	return parsePendingClientResolution(
		sessionStorage.getItem(PENDING_RESOLUTION_KEY),
	);
}

/** Returns the pending request and clears it, so one sign-in yields one code. */
export function takeClientAuthorization(): ClientAuthorizationRequest | null {
	const pending = pendingClientAuthorization();
	forgetClientAuthorization();
	return pending;
}

/** Where the person approves a client by name. */
export const CONSENT_PATH = "/oauth2/consent";

/**
 * Completes a pending handoff, if there is one, and reports whether it took the
 * browser away. Called wherever a sign-in has just produced a session — the
 * host's own session, which is what authorizes the code the client receives.
 *
 * When the host said the request needs consent, this navigates to the consent
 * page instead of issuing a code, and still reports true: the browser HAS been
 * moved, just not to the client yet. Returning false there would navigate the
 * person into the product on top of a client still waiting for its redirect.
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
	// Fail closed. A pending request whose resolution did not survive — the host
	// never answered, the browser cleared it, a response shape changed — must not
	// be completed silently: that would be a code issued for a client whose
	// approval requirement nobody could read. Send the person to consent, where
	// the absence is visible and nothing is granted without them.
	const resolution = pendingClientResolution();
	if (!resolution || resolution.requiresConsent) {
		// replace, not assign: Back must not return the person to a half-finished
		// sign-in. It is a full navigation rather than a router push because this
		// runs inside the auth provider, which has no router — and the session it
		// just established lives in the refresh cookie, which the consent page
		// bootstraps from exactly as any other entry to the app does.
		window.location.replace(CONSENT_PATH);
		return true;
	}
	await grantClientAuthorization(request, accessToken);
	return true;
}

/**
 * Issues the code and sends the browser back to the client. Separated from the
 * decision above so the consent page can call it on approval.
 */
export async function grantClientAuthorization(
	request: ClientAuthorizationRequest,
	accessToken: string | null,
	options: { consentGranted?: boolean } = {},
): Promise<void> {
	const response = await fetch("/v1/oauth2/authorize/grant", {
		method: "POST",
		headers: {
			"Content-Type": "application/json",
			...(accessToken ? { Authorization: `Bearer ${accessToken}` } : {}),
		},
		credentials: "include",
		body: JSON.stringify({
			...oauthWireFormat(request),
			// Stated only by the consent page, after the person pressed Allow. The
			// host refuses a request that needs consent and does not carry it, so a
			// client-side defect — a mis-decoded `requires_consent`, a dropped
			// navigation — can no longer skip the screen silently. It is not a
			// security boundary against a hostile browser (a browser that holds the
			// session can say anything); it is what stops a bug in this file from
			// quietly issuing credentials nobody approved.
			consent_granted: options.consentGranted === true,
		}),
	});
	if (!response.ok) {
		throw new Error(
			"Sign-in succeeded but the application could not be authorized.",
		);
	}
	const granted = parseOAuthGrant(await response.json().catch(() => null));
	if (!granted) {
		throw new Error(
			"Sign-in succeeded but the application could not be authorized.",
		);
	}
	// The redirect target is the one the host validated against its registry, so
	// it is used verbatim; a URL assembled from anything the page still holds
	// would be assembling it from the caller's own input again.
	forgetClientAuthorization();
	window.location.replace(
		oauthRedirect(request.redirectUri, {
			code: granted.code,
			state: request.state || undefined,
			// RFC 9207: naming the issuer lets a client with more than one
			// configured authorization server refuse a code that came back from
			// the wrong one.
			iss: granted.issuer,
		}),
	);
}

/**
 * The person declined. The client is told so in the terms it understands
 * (`access_denied`) rather than being left on a redirect that never arrives.
 */
export function declineClientAuthorization(
	request: ClientAuthorizationRequest,
): void {
	// RFC 9207 §2 covers error responses too, and the host advertises support for
	// it: a client enforcing that protection cannot authenticate a decline that
	// carries no `iss`. The issuer is what the HOST answered, never anything from
	// the request, so a caller cannot choose what the client is told.
	const issuer = pendingClientResolution()?.issuer;
	forgetClientAuthorization();
	window.location.replace(
		oauthRedirect(request.redirectUri, {
			error: "access_denied",
			error_description: "the person declined this authorization",
			state: request.state || undefined,
			iss: issuer,
		}),
	);
}
