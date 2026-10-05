import { getEndpoints } from "codefly";

import {
	duplicatedOAuthParameter,
	oauthQueryString,
	oauthRedirect,
	oauthWireFormat,
	parseOAuthRefusal,
	readOAuthAuthorizationRequest,
} from "@/features/auth/model/oauth-authorization";
import { resolveCodeflyGatewayContext } from "@/lib/codefly-gateway-context";
import { INTERNAL_TOKEN_HEADER } from "@/lib/internal-token";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

/**
 * `GET /oauth2/authorize` — the RFC 6749 authorization endpoint (issue #1003).
 *
 * It lives in this app because the host is the only sign-in UI: an
 * authorization request ends in a login page and, where the client is not one
 * the operator declared or the request names a resource, a consent screen.
 * Neither is a response body, so neither can live in accounts.
 *
 * What this handler does and does not do matters:
 *
 *   - It validates the whole request against accounts BEFORE redirecting
 *     anywhere. A request that cannot succeed is refused while the browser is
 *     still on the host and the person has typed nothing.
 *   - It delivers a refusal to the client's redirect URI only once accounts
 *     has confirmed that URI belongs to that client (RFC 6749 §4.1.2.1). Before
 *     that, the refusal is rendered here. Redirecting to an unvalidated
 *     redirect_uri would make this an open redirector that any site could use.
 *   - It never issues the code. That happens after sign-in and consent, from
 *     the person's own session (see /v1/oauth2/authorize/grant).
 */
export async function GET(request: Request): Promise<Response> {
	const url = new URL(request.url);
	// A duplicated singleton parameter is refused before anything is read from
	// it: `get` would return the first and discard the rest, which is the host
	// choosing between two values the caller sent. Refused HERE rather than at
	// accounts, because by the time the request is reduced to a JSON object the
	// duplicate no longer exists to notice.
	const duplicated = duplicatedOAuthParameter(url.searchParams);
	if (duplicated) {
		return invalidRequestPage(
			"This sign-in link repeats a parameter that may appear only once.",
		);
	}
	const authorization = readOAuthAuthorizationRequest(url.searchParams);
	if (!authorization) {
		return invalidRequestPage(
			"This sign-in link is incomplete: client_id, redirect_uri and code_challenge are all required.",
		);
	}

	const base = gatewayBase();
	if (!base) {
		return invalidRequestPage(
			"Sign-in is temporarily unavailable. Please try again in a few minutes.",
			503,
		);
	}

	const headers = new Headers({ "content-type": "application/json" });
	// The authorization server needs to know which origin it is being reached at,
	// because every URL it publishes names it — the issuer in its metadata, the
	// redirect it binds a code to, the link it emails. That origin is OPERATOR
	// CONFIGURATION and is resolved from it here, never read off this request: the
	// internal token stamped beside it proves which PROCESS is asking, which is
	// not the same as the origin being this host's, so a caller-supplied host
	// travelling under that token would have been published as the issuer's.
	const gatewayContext = resolveCodeflyGatewayContext();
	if (gatewayContext) {
		headers.set(INTERNAL_TOKEN_HEADER, gatewayContext.internalToken);
		headers.set(PUBLIC_ORIGIN_HEADER, gatewayContext.publicOrigin);
	}

	let response: Response;
	try {
		response = await fetch(`${base}/v1/oauth2/authorize/validate`, {
			method: "POST",
			headers,
			body: JSON.stringify(oauthWireFormat(authorization)),
			cache: "no-store",
			redirect: "manual",
		});
	} catch (err) {
		console.error("oauth authorize: validation call failed", err);
		return invalidRequestPage(
			"Sign-in is temporarily unavailable. Please try again in a few minutes.",
			503,
		);
	}

	if (!response.ok) {
		const refusal = parseOAuthRefusal(await response.json().catch(() => null));
		if (refusal?.redirectable) {
			// The redirect URI is this client's own, so the client — which is
			// sitting on a loopback listener waiting — is told what went wrong
			// rather than being left on a browser tab that never comes back.
			return redirectTo(
				oauthRedirect(authorization.redirectUri, {
					error: refusal.error,
					error_description: refusal.errorDescription,
					state: authorization.state || undefined,
					// RFC 9207 §2 covers error responses, and this host advertises
					// support for the parameter. The issuer comes from the host's
					// own answer, never from the request, so a caller cannot choose
					// what its client is told about who refused it.
					iss: refusal.issuer,
				}),
			);
		}
		return invalidRequestPage(
			refusal?.errorDescription ??
				"This application is not registered to sign in here, or asked to be returned to an address it has not registered.",
			response.status === 503 ? 503 : 400,
		);
	}

	// Accepted. Hand the browser to the host's own login page, carrying the
	// request verbatim: the page signs the person in however this deployment is
	// configured, shows consent when the host said to, and completes through the
	// grant call. The parameters are re-rendered from the parsed request rather
	// than forwarded as the original query string, so anything this host does
	// not read cannot ride along into the page's URL.
	const login = new URL("/auth/login", url);
	login.search = oauthQueryString(authorization);
	return redirectTo(login.toString());
}

/**
 * A 302 built explicitly rather than with `Response.redirect`, whose headers
 * are immutable — the referrer policy is load-bearing here. The authorization
 * request carries the client's PKCE challenge and `state` in the query, and a
 * referrer would leak both to every subresource of the page being redirected
 * to.
 */
function redirectTo(location: string): Response {
	return new Response(null, {
		status: 302,
		headers: {
			location,
			"referrer-policy": "no-referrer",
			"cache-control": "no-store",
		},
	});
}

/**
 * Resolve the auth-gateway REST base from the Codefly SDK, normalised the way
 * every other server-side caller here normalises it: the whole address minus a
 * trailing slash, never its origin, so a base path on the endpoint survives.
 */
function gatewayBase(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	);
	if (!endpoint?.address) return null;
	try {
		return new URL(endpoint.address).toString().replace(/\/$/, "");
	} catch {
		return null;
	}
}

/**
 * A refusal the person has to read, because it cannot be delivered to the
 * client. Plain text with no markup and no echo of anything the caller sent:
 * this page is reachable by any site that can make a browser follow a link, so
 * reflecting a parameter here would be a cross-site scripting surface on the
 * host's own origin.
 */
function invalidRequestPage(message: string, status = 400): Response {
	return new Response(message, {
		status,
		headers: {
			"content-type": "text/plain; charset=utf-8",
			"cache-control": "no-store",
			"referrer-policy": "no-referrer",
		},
	});
}
