import {
	getCurrentModule,
	getCurrentService,
	getEndpoints,
	resolveServiceAddressSync,
} from "codefly";
import { NextRequest } from "next/server";
import { describe, expect, test } from "vitest";
import { resolveCodeflyGatewayContext } from "@/lib/codefly-gateway-context";
import { trustedGatewayRequestHeaders } from "@/proxy";
import { OnboardingStepStatus } from "../model/types";
import { OnboardingBackend } from "./backend";

const INTERNAL_TOKEN_HEADER = "X-Codefly-Internal-Token";
const PUBLIC_ORIGIN_HEADER = "X-Codefly-Public-Origin";

// The private product gateway. In the browser this is never addressed directly:
// Next rewrites the same-origin product API namespaces onto it. This headless
// suite runs without a Next server, so it addresses the gateway itself and
// reproduces the proxy's stamping with the real production libraries below.
function requiredProductGateway(): string {
	const currentModule = getCurrentModule();
	const injected = getEndpoints().filter(
		(endpoint) =>
			endpoint.service === "auth-sidecar" &&
			endpoint.name === "rest" &&
			endpoint.protocol === "REST" &&
			(!currentModule || endpoint.module === currentModule),
	);
	if (injected.length === 1 && injected[0].address) {
		return injected[0].address;
	}
	if (injected.length > 1) {
		throw new Error(
			"Codefly injected multiple auth-sidecar/rest endpoints into the frontend test runtime.",
		);
	}

	// Supports running the file directly for local diagnostics. The normal
	// Codefly test path always uses the dependency endpoint injected above,
	// which remains correct under isolated naming scopes.
	const endpoint = resolveServiceAddressSync("auth-sidecar", "rest", {
		cwd: process.cwd(),
	});
	if (!endpoint) {
		throw new Error(
			"Codefly did not resolve auth-sidecar/rest; run this through " +
				"`codefly test service frontend --suite integration`.",
		);
	}
	return endpoint;
}

// The frontend's own HTTP origin is the module's public product entry, and it is
// the origin the gateway must end up treating as verified. It comes from the
// SDK, never from a literal or an assumed port.
function requiredProductOrigin(): string {
	const currentModule = getCurrentModule();
	const currentService = getCurrentService();
	const injected = getEndpoints().filter(
		(endpoint) =>
			endpoint.name === "http" &&
			endpoint.protocol === "HTTP" &&
			(!currentModule || endpoint.module === currentModule) &&
			(!currentService || endpoint.service === currentService),
	);
	if (injected.length !== 1 || !injected[0].address) {
		throw new Error(
			"Codefly did not inject exactly one frontend/http endpoint into the frontend test runtime.",
		);
	}
	return injected[0].address;
}

// Reproduce, through the real proxy and gateway-context libraries, the headers a
// same-origin product API request carries once Next has stamped it. Nothing here
// reads a carrier variable or hardcodes a token: the internal-auth secret and the
// public origin both come from the Codefly SDK.
function stampedGatewayHeaders(): Record<string, string> {
	const productOrigin = requiredProductOrigin();
	const context = resolveCodeflyGatewayContext(productOrigin);
	if (!context) {
		throw new Error(
			"Codefly did not provide the frontend gateway context (internal-auth secret and frontend/http endpoint).",
		);
	}
	const stamped = trustedGatewayRequestHeaders(
		new NextRequest(
			new URL("/saas.accounts.v1.OnboardingService/GetProgress", productOrigin),
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

describe("headless frontend onboarding pipeline", () => {
	test("drives real organization, invitation, billing, API-key, events, and progress persistence", async () => {
		const suffix = crypto.randomUUID().replaceAll("-", "").slice(0, 12);
		const gatewayBaseURL = requiredProductGateway();
		const backend = new OnboardingBackend({
			connectBaseUrl: gatewayBaseURL,
			restBaseUrl: gatewayBaseURL,
			trustedGatewayHeaders: stampedGatewayHeaders(),
		});

		try {
			await backend.authenticateFixture("dev-admin");
		} catch (error) {
			throw new Error(
				`Cannot authenticate through Codefly product gateway ${gatewayBaseURL}`,
				{ cause: error },
			);
		}
		const result = await backend.completePipeline({
			organizationName: `Pipeline ${suffix}`,
			organizationSlug: `pipeline-${suffix}`,
			inviteEmail: `invite-${suffix}@example.com`,
			apiKeyName: `pipeline-${suffix}`,
		});

		expect(result.progress.organizationId).toBe(result.organizationId);
		expect(result.progress.requiredComplete).toBe(true);
		expect(result.progress.checklistComplete).toBe(true);
		expect(result.progress.steps).toHaveLength(4);
		expect(
			result.progress.steps.every(
				(step) => step.status === OnboardingStepStatus.COMPLETED,
			),
		).toBe(true);

		const persisted = await backend.getProgress(result.organizationId);
		expect(persisted).toEqual(result.progress);
	}, 120_000);
});
