import "server-only";

import { timingSafeEqual } from "node:crypto";

import { getWorkspaceSecret } from "codefly";

export const INTERNAL_TOKEN_HEADER = "x-codefly-internal-token";

/**
 * The cluster-internal credential a caller must present on a route that is not
 * public. Same secret the frontend uses to prove a trusted origin to the
 * gateway. Returns null when unset so those routes fail closed.
 */
export function expectedInternalToken(): string | null {
	const token = getWorkspaceSecret(
		"internal-auth",
		"CODEFLY_INTERNAL_TOKEN",
	)?.trim();
	return token ? token : null;
}

/**
 * Whether a request carries the cluster-internal token. Fails closed when the
 * secret is unset: without it there is nothing to compare against, so no caller
 * can be trusted.
 */
export function isTrustedInternalCall(request: Request): boolean {
	const expected = expectedInternalToken();
	if (!expected) {
		return false;
	}
	const presented = request.headers.get(INTERNAL_TOKEN_HEADER) ?? "";
	const presentedBytes = Buffer.from(presented, "utf8");
	const expectedBytes = Buffer.from(expected, "utf8");
	if (presentedBytes.length !== expectedBytes.length) {
		return false;
	}
	return timingSafeEqual(presentedBytes, expectedBytes);
}
