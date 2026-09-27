import "server-only";

import { getEndpoints, getWorkspaceSecret } from "codefly";

/**
 * What the calling viewer may use (issue #949).
 *
 * The solution projections used to answer the same deployment-wide set to every
 * caller. Narrowing them needs two things this process cannot establish on its
 * own: the viewer's verified organization, and which installed solutions that
 * viewer was granted.
 *
 * Neither may be derived here. `lib/auth-session.ts` can decode an access token,
 * but `decodeJWTPayload` only base64-decodes it — it verifies NOTHING — so an
 * organization read from a claim in a route handler is an organization the caller
 * chose, and reading another tenant's menu would be a matter of editing one
 * field. These routes are also not proxied through the gateway (`src/proxy.ts`
 * forwards `/v1/*` and `/saas.accounts.v1.*`; every `/api/*` path is served
 * in-process), so nothing stamps a verified identity on the incoming request
 * either — and a caller-supplied `x-org-id` is exactly what this app strips
 * elsewhere.
 *
 * So the question is asked of the component that authenticates. The gateway runs
 * `ext_authz` on the credential this module forwards, projects the organization
 * and viewer from it, and answers from the installation set and the scope-grant
 * union accounts resolves in one transaction. What comes back is verified
 * identity plus authority — not an echo of anything a browser said.
 */

/** One installed solution the viewer may use. */
export interface SolutionEntitlement {
	/** The registered solution id this entitlement admits. */
	id: string;
	/**
	 * Whether the installation is healthy right now. An unhealthy one is still
	 * entitled — the org installed it and the viewer was granted it — so it stays
	 * in the projection marked unavailable rather than disappearing, which would
	 * send someone looking for a grant that already exists.
	 */
	healthy: boolean;
	/** The scope node whose grant admitted it. */
	scopeNodeId: string;
}

/** The verified viewer, and everything that viewer may use. */
export interface ViewerEntitlements {
	/** The organization the GATEWAY verified — never one this process decoded. */
	org: string;
	/** The viewer the gateway verified. */
	viewer: string;
	byId: Map<string, SolutionEntitlement>;
	/**
	 * A fingerprint of the entitled set, for a consumer keying a cache on it.
	 *
	 * It is computed from the answer rather than reported by the authority: a
	 * digest of exactly the rows received is exact by construction, while a
	 * server-side revision would have to describe either one page or a set larger
	 * than was read. A grant, a revoke, an install, an uninstall or a health change
	 * all move it, because all of them change these rows — and a grant that changes
	 * none of them leaves it still, which is correct: the projection really is
	 * identical.
	 */
	revision: string;
}

/**
 * Why an entitlement read could not be answered. `unauthenticated` is the
 * caller's own credential being refused, which a projection must relay as 401
 * rather than as an empty list: `[]` would read as "nothing is offered for you"
 * and is indistinguishable from "your organization installed nothing".
 * `unavailable` is this replica being unable to ask at all — a 503, for the same
 * reason an unreadable registry is one.
 */
export type EntitlementFailure =
	| "unauthenticated"
	| "forbidden"
	| "unavailable";

const GATEWAY_ENTITLEMENTS_PATH = "/solutions/_entitlements";
const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";

/**
 * How long the gateway gets to answer. It asks accounts under its own 10s budget,
 * so this is a little longer; the point is that it is finite. A viewer's menu
 * blocks on it, so a stalled authority must become a failed projection rather
 * than a hung page.
 */
const ENTITLEMENTS_READ_TIMEOUT_MS = 12_000;

/**
 * The auth-gateway origin, from Codefly service discovery. It must NOT come from
 * the incoming request's Host header: routing a server-side fetch through a
 * client-controlled host is an SSRF sink. Same rule, same reason, as the registry
 * snapshot read in registry.ts.
 */
function gatewayOrigin(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	);
	if (!endpoint?.address) return null;
	try {
		return new URL(endpoint.address).origin;
	} catch {
		return null;
	}
}

function internalToken(): string | null {
	const token = getWorkspaceSecret(
		"internal-auth",
		"CODEFLY_INTERNAL_TOKEN",
	)?.trim();
	return token ? token : null;
}

interface GatewayEntitlementEntry {
	id?: unknown;
	healthy?: unknown;
	scopeNodeId?: unknown;
}

/**
 * A stable digest of the entitled set.
 *
 * Sorted before hashing so the fingerprint depends on the SET and not on the
 * order it arrived in: the authority orders by installation id, which is stable
 * but says nothing about solution id, and a reordering that changed the key would
 * evict a still-correct cache entry on every read. `healthy` is part of the
 * digest because it is part of the projection — a solution going unhealthy must
 * re-render as unavailable.
 */
function entitlementRevision(entitlements: SolutionEntitlement[]): string {
	const canonical = entitlements
		.map(
			(entitlement) =>
				`${entitlement.id}\u0000${entitlement.scopeNodeId}\u0000${entitlement.healthy ? "1" : "0"}`,
		)
		.sort()
		.join("\u0001");
	// FNV-1a over the canonical form. This is a cache key, never a security
	// boundary — nothing authorizes on it, and a collision costs a stale
	// projection for one (org, viewer) until the next change, not an escalation.
	// A cryptographic digest would mean an async subtle-crypto call on the path of
	// every menu read for no property that is used.
	let hash = 0x811c9dc5;
	for (let index = 0; index < canonical.length; index += 1) {
		hash ^= canonical.charCodeAt(index);
		hash = Math.imul(hash, 0x01000193);
	}
	// Length travels with the hash so two different sets cannot share a key on the
	// 32-bit value alone.
	return `${(hash >>> 0).toString(16)}.${entitlements.length}`;
}

function parseEntitlements(body: unknown): ViewerEntitlements | null {
	if (typeof body !== "object" || body === null) return null;
	const { org, viewer, solutions } = body as {
		org?: unknown;
		viewer?: unknown;
		solutions?: unknown;
	};
	// The organization and viewer are the point of this answer: without them the
	// caller has a list it cannot attribute, and a cache it cannot key. A response
	// missing either is malformed, not an empty entitlement set.
	if (typeof org !== "string" || org === "") return null;
	if (typeof viewer !== "string" || viewer === "") return null;
	if (!Array.isArray(solutions)) return null;
	const parsed: SolutionEntitlement[] = [];
	for (const entry of solutions as GatewayEntitlementEntry[]) {
		if (typeof entry !== "object" || entry === null) return null;
		if (typeof entry.id !== "string" || entry.id === "") return null;
		if (typeof entry.healthy !== "boolean") return null;
		parsed.push({
			id: entry.id,
			healthy: entry.healthy,
			scopeNodeId:
				typeof entry.scopeNodeId === "string" ? entry.scopeNodeId : "",
		});
	}
	return {
		org,
		viewer,
		byId: new Map(parsed.map((entitlement) => [entitlement.id, entitlement])),
		revision: entitlementRevision(parsed),
	};
}

/**
 * Ask the gateway what the credential on `request` may use.
 *
 * The caller's `authorization` header is forwarded verbatim and is the only thing
 * that decides whose entitlements come back. It is deliberately not inspected
 * here: which credentials authenticate — a session bearer, an API key, a
 * registered client's token — is the gateway's judgement, and a second opinion in
 * this process would be a second perimeter to keep in step. A request with no
 * credential is forwarded too, so the 401 comes from the one component that
 * authenticates rather than from a guess made here.
 */
export async function viewerEntitlements(
	request: Request,
): Promise<ViewerEntitlements | EntitlementFailure> {
	const origin = gatewayOrigin();
	const token = internalToken();
	if (!origin || !token) {
		console.error(
			"solution entitlements: gateway endpoint or internal token unresolved",
		);
		return "unavailable";
	}
	const headers = new Headers({
		[INTERNAL_TOKEN_HEADER]: token,
		accept: "application/json",
	});
	const presented = request.headers.get("authorization");
	if (presented) headers.set("authorization", presented);

	let response: Response;
	try {
		response = await fetch(`${origin}${GATEWAY_ENTITLEMENTS_PATH}`, {
			headers,
			cache: "no-store",
			signal: AbortSignal.timeout(ENTITLEMENTS_READ_TIMEOUT_MS),
		});
	} catch (err) {
		console.error("solution entitlements: gateway unreachable", err);
		return "unavailable";
	}
	if (response.status === 401) return "unauthenticated";
	// A credential that authenticated but carries no organization, or one the
	// authority refused. It is distinct from 401: re-authenticating would not
	// change it, so a consumer must not be sent back through login.
	if (response.status === 403) return "forbidden";
	if (!response.ok) {
		console.error(`solution entitlements: gateway answered ${response.status}`);
		return "unavailable";
	}
	const parsed = parseEntitlements(await response.json().catch(() => null));
	if (parsed === null) {
		console.error("solution entitlements: malformed gateway answer");
		return "unavailable";
	}
	return parsed;
}
