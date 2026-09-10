import { NextRequest } from "next/server";
import { resolveCodeflyGatewayContext } from "@/lib/codefly-gateway-context";
import { INTERNAL_TOKEN_HEADER } from "@/lib/internal-token";
import { trustedGatewayRequestHeaders } from "@/proxy";
import { productOrigin } from "@/test/codefly-endpoints";

// Endpoint resolution lives in one place, shared with the Playwright config and
// the e2e global setup: see ./codefly-endpoints.
export {
	codeflyInjectedRuntime,
	type PipelineRuntimeReader,
	productGatewayURL,
	productOrigin,
} from "@/test/codefly-endpoints";

export { INTERNAL_TOKEN_HEADER };
export const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

/**
 * Reproduce, through the real proxy and gateway-context libraries, the headers
 * a same-origin product API request carries once Next has stamped it. Nothing
 * here hardcodes a token: the internal-auth secret and the public origin both
 * come from the Codefly SDK.
 */
export function stampedGatewayHeaders(): Record<string, string> {
	const origin = productOrigin();
	const context = resolveCodeflyGatewayContext(origin);
	if (!context) {
		throw new Error(
			"Codefly did not provide the frontend gateway context (internal-auth secret and frontend/http endpoint).",
		);
	}
	const stamped = trustedGatewayRequestHeaders(
		new NextRequest(
			new URL("/saas.accounts.v1.OnboardingService/GetProgress", origin),
		),
		context,
	);
	if (!stamped) {
		throw new Error(
			"The frontend proxy did not stamp trust headers on a product API request.",
		);
	}
	const internalToken = stamped.get(INTERNAL_TOKEN_HEADER);
	const publicOrigin = stamped.get(PUBLIC_ORIGIN_HEADER);
	if (!internalToken || !publicOrigin) {
		throw new Error("The frontend proxy stamped an incomplete trust context.");
	}
	return {
		[INTERNAL_TOKEN_HEADER]: internalToken,
		[PUBLIC_ORIGIN_HEADER]: publicOrigin,
	};
}

/** Collision-free suffix so pipeline tests can share one database safely. */
export function uniqueSuffix(): string {
	return crypto.randomUUID().replaceAll("-", "").slice(0, 12);
}
