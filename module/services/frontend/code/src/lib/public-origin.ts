import "server-only";

import { resolveVerifiedPublicOrigin } from "@/lib/codefly-gateway-context";

/**
 * The origin this deployment treats as its own, from operator configuration.
 *
 * It comes from `application/APP_BASE_URL`, or from a render that carries a real
 * ingress host, and from nothing else — never from a request, because an origin a
 * caller can name is not this deployment's origin. See `resolveVerifiedPublicOrigin`,
 * which refuses rather than guessing.
 *
 * Returns undefined when this deployment has no verified public origin, which
 * every caller must treat as a refusal.
 */
export function configuredPublicOrigin(): string | undefined {
	return resolveVerifiedPublicOrigin();
}
