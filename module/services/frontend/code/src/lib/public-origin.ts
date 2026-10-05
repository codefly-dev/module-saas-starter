import "server-only";

import { resolveVerifiedPublicOrigin } from "@/lib/codefly-gateway-context";

/**
 * The origin this deployment treats as its own, from operator configuration.
 *
 * It used to be derived from the request — the ingress-set forwarded pair, or the
 * pod-local URL — which made it whatever a caller said: a request bearing
 * `X-Forwarded-Host: evil.example` was answered as though the product were served
 * there, and the solution proxy compared the browser's `Origin` against it, so a
 * cross-site request that supplied both passed the same-origin check. The origin
 * now comes from `application/APP_BASE_URL`, or from a render that carries a real
 * ingress host, and from nothing else; see
 * `resolveVerifiedPublicOrigin`, which refuses rather than guessing.
 *
 * Returns undefined when this deployment has no verified public origin, which
 * every caller must treat as a refusal.
 */
export function configuredPublicOrigin(): string | undefined {
	return resolveVerifiedPublicOrigin();
}
