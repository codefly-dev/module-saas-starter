import "server-only";

import { createPublicKey, type KeyObject, verify } from "node:crypto";

import { getEndpoints } from "codefly";

/**
 * Owner-bound authority for solution registration — the frontend half.
 *
 * The gateway and this host accept the SAME credential: a short-lived,
 * solution-bound token accounts mints against the publisher's own registration
 * secret. Both sides verify it independently against the cluster's published
 * key set, so neither has to hold a secret every registrant shares, and neither
 * has to trust the other's decision.
 *
 * This half matters more than the gateway's, not less: a frontend registration
 * decides which script the browser loads as a Module-Federation remote, and that
 * code then runs in the host origin with the viewer's access token. See
 * `module/SOLUTION_REGISTRATION.md` for the trust contract that follows from it.
 */

/** Header carrying the signed per-solution registration token. */
export const SOLUTION_REGISTRATION_HEADER = "x-codefly-solution-registration";

/**
 * Audience scoping the token to this single purpose. An access token carries
 * `saas-starter` and a module-registration token `module-registration`; even
 * though all three are signed with the same key, the audience check makes them
 * non-interchangeable — no access token can publish host-origin code.
 */
const REGISTRATION_AUDIENCE = "solution-registration";

/** Issuer accounts stamps on every token it mints (see the gateway's ExtAuthz). */
const ISSUER = "saas-starter";

/** Tolerance for clock skew between accounts and this host, in seconds. */
const CLOCK_SKEW_LEEWAY_SECONDS = 60;

/** The gateway's public JWKS route, which republishes accounts' key set. */
const JWKS_PATH = "/v1/auth/.well-known/jwks.json";

const JWKS_REQUEST_TIMEOUT_MS = 2_000;
const JWKS_MAX_BYTES = 256 * 1024;
/** Minimum spacing between refetches triggered by an unrecognised key id. */
const JWKS_PROBE_INTERVAL_MS = 5_000;

export interface SolutionRegistrationClaims {
	/** Publisher identity accountable for the registration. */
	subject: string;
	/** The single solution id this credential may register, update, or delete. */
	solution: string;
	/** Token id, burned on use so a captured credential cannot be replayed. */
	jti: string;
	/** Expiry, in epoch seconds. */
	expiresAt: number;
}

function decodeSegment(segment: string): unknown {
	return JSON.parse(Buffer.from(segment, "base64url").toString("utf8"));
}

interface JwksState {
	keys: Map<string, KeyObject>;
	fetchedAt: number;
}

const globalForJwks = globalThis as typeof globalThis & {
	__solutionRegistrationJwks?: JwksState;
	__solutionRegistrationJtis?: Map<string, number>;
};

/** Resolve the auth-gateway REST base from the Codefly SDK. */
function gatewayBase(): string | null {
	const endpoint = getEndpoints().find(
		(candidate) =>
			candidate.service === "auth-gateway" && candidate.name === "rest",
	);
	if (!endpoint?.address) {
		return null;
	}
	try {
		return new URL(endpoint.address).origin;
	} catch {
		return null;
	}
}

/**
 * Fetch and parse the published key set, or null when it could not be read. Only
 * Ed25519 signing keys are imported: the credential is EdDSA and nothing else is
 * accepted, so a key set that grew an RSA entry cannot widen what this host will
 * verify.
 *
 * Every failure — unreachable, non-2xx, oversized, unparseable, or carrying no
 * usable key — returns null rather than an empty map, because the caller must be
 * able to tell "the key set is now empty" (which no legitimate rotation
 * produces) from "I could not read it", and only the latter may keep the keys it
 * already has.
 */
async function fetchKeys(): Promise<Map<string, KeyObject> | null> {
	const base = gatewayBase();
	if (!base) {
		return null;
	}
	const response = await fetch(`${base}${JWKS_PATH}`, {
		signal: AbortSignal.timeout(JWKS_REQUEST_TIMEOUT_MS),
		cache: "no-store",
	});
	if (!response.ok) {
		return null;
	}
	const body = await response.text();
	if (body.length > JWKS_MAX_BYTES) {
		return null;
	}
	const parsed = JSON.parse(body) as { keys?: unknown };
	const keys = new Map<string, KeyObject>();
	if (!Array.isArray(parsed.keys)) {
		return null;
	}
	for (const entry of parsed.keys) {
		const jwk = entry as Record<string, unknown>;
		if (
			jwk.kty !== "OKP" ||
			jwk.crv !== "Ed25519" ||
			typeof jwk.kid !== "string" ||
			typeof jwk.x !== "string"
		) {
			continue;
		}
		try {
			keys.set(
				jwk.kid,
				createPublicKey({
					key: { kty: "OKP", crv: "Ed25519", x: jwk.x },
					format: "jwk",
				}),
			);
		} catch {
			// A malformed entry is skipped, not fatal: one bad key must not take
			// the rest of a rotating key set out of service.
		}
	}
	return keys.size > 0 ? keys : null;
}

/**
 * The verification key for `kid`, refetching at most once per probe interval
 * when the id is unrecognised — which is what a key rotation looks like from
 * here. Returns null when the key set cannot be reached, so verification fails
 * closed rather than falling back to an older trust decision.
 */
async function keyFor(kid: string): Promise<KeyObject | null> {
	const cached = globalForJwks.__solutionRegistrationJwks;
	const hit = cached?.keys.get(kid);
	if (hit) {
		return hit;
	}
	if (cached && Date.now() - cached.fetchedAt < JWKS_PROBE_INTERVAL_MS) {
		return null;
	}
	let fetched: Map<string, KeyObject> | null = null;
	try {
		fetched = await fetchKeys();
	} catch {
		fetched = null;
	}
	// A refresh that FAILED must not discard keys that still verify: a single
	// non-2xx during a gateway rollout would otherwise replace a good key set
	// with an empty one and refuse every registration until some later fetch
	// happened to succeed. Keep what we hold and only throttle the next attempt;
	// a successful fetch replaces the set outright, which is what a rotation
	// needs.
	const keys = fetched ?? cached?.keys ?? new Map<string, KeyObject>();
	globalForJwks.__solutionRegistrationJwks = { keys, fetchedAt: Date.now() };
	return keys.get(kid) ?? null;
}

/**
 * Verify a presented registration credential. Fails closed on every defect: a
 * missing or malformed token, an algorithm other than EdDSA, an unknown or
 * unreachable key, a bad signature, the wrong issuer or audience, an expired or
 * not-yet-valid token, or a token with no jti, subject, or solution binding.
 */
export async function verifySolutionRegistration(
	token: string | null,
): Promise<SolutionRegistrationClaims | null> {
	if (!token) {
		return null;
	}
	const parts = token.split(".");
	if (parts.length !== 3) {
		return null;
	}
	const [headerSegment, payloadSegment, signatureSegment] = parts;
	let header: Record<string, unknown>;
	let payload: Record<string, unknown>;
	try {
		header = decodeSegment(headerSegment) as Record<string, unknown>;
		payload = decodeSegment(payloadSegment) as Record<string, unknown>;
	} catch {
		return null;
	}
	// Alg-locked: the key set holds only Ed25519 keys, and "none" or a symmetric
	// alg must never reach a verify call.
	if (header.alg !== "EdDSA" || typeof header.kid !== "string") {
		return null;
	}
	const key = await keyFor(header.kid);
	if (!key) {
		return null;
	}
	let signatureValid = false;
	try {
		signatureValid = verify(
			null,
			Buffer.from(`${headerSegment}.${payloadSegment}`, "utf8"),
			key,
			Buffer.from(signatureSegment, "base64url"),
		);
	} catch {
		return null;
	}
	if (!signatureValid) {
		return null;
	}

	const audience = payload.aud;
	const audiences = Array.isArray(audience) ? audience : [audience];
	if (
		payload.iss !== ISSUER ||
		!audiences.includes(REGISTRATION_AUDIENCE) ||
		typeof payload.sub !== "string" ||
		payload.sub === "" ||
		typeof payload.solution !== "string" ||
		payload.solution === "" ||
		typeof payload.jti !== "string" ||
		payload.jti === "" ||
		typeof payload.exp !== "number"
	) {
		return null;
	}
	const now = Math.floor(Date.now() / 1000);
	if (payload.exp + CLOCK_SKEW_LEEWAY_SECONDS < now) {
		return null;
	}
	if (
		typeof payload.nbf === "number" &&
		payload.nbf - CLOCK_SKEW_LEEWAY_SECONDS > now
	) {
		return null;
	}
	return {
		subject: payload.sub,
		solution: payload.solution,
		jti: payload.jti,
		expiresAt: payload.exp,
	};
}

/**
 * Burn a credential's jti, reporting false when it has already been used. A
 * token is minted per attempt and lives minutes, so the set to remember is small
 * and self-clearing: an entry is dropped once the token it names could no longer
 * verify anyway.
 */
export function consumeRegistrationToken(
	claims: SolutionRegistrationClaims,
): boolean {
	const used =
		globalForJwks.__solutionRegistrationJtis ??
		(globalForJwks.__solutionRegistrationJtis = new Map<string, number>());
	const now = Math.floor(Date.now() / 1000);
	for (const [jti, expiry] of used) {
		if (expiry + CLOCK_SKEW_LEEWAY_SECONDS < now) {
			used.delete(jti);
		}
	}
	if (used.has(claims.jti)) {
		return false;
	}
	used.set(claims.jti, claims.expiresAt);
	return true;
}
