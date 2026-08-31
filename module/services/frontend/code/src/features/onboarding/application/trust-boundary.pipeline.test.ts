// Fail-closed checks for the frontend → auth-gateway → Accounts trust chain,
// against the real dependency graph.
//
// These cannot be written against a mocked fetch: the whole point is what the
// *gateway and Accounts* do with headers, not what the client sends. A stubbed
// transport would happily accept every request below.
//
// The regression that motivated them: with no APP_BASE_URL configured (as in
// this tier), Accounts derives interactive links from the verified public origin.
// When that origin is absent the invitation flow must refuse rather than invent
// one. (A configured APP_BASE_URL, when present, takes precedence as the trusted
// origin; this tier does not set one.)

import { describe, expect, test } from "vitest";
import {
	INTERNAL_TOKEN_HEADER,
	PUBLIC_ORIGIN_HEADER,
	productGatewayURL,
	stampedGatewayHeaders,
	uniqueSuffix,
} from "@/test/pipeline-gateway";
import { OnboardingBackend } from "./backend";

const AUTHENTICATE = "/saas.accounts.v1.AuthService/Authenticate";
const CREATE_ORGANIZATION =
	"/saas.accounts.v1.OrganizationService/CreateOrganization";

function connectRequest(
	gateway: string,
	procedure: string,
	body: unknown,
	headers: Record<string, string> = {},
): Promise<Response> {
	return fetch(`${gateway}${procedure}`, {
		method: "POST",
		headers: { "Content-Type": "application/json", ...headers },
		body: JSON.stringify(body),
	});
}

async function authenticatedBackend(): Promise<OnboardingBackend> {
	const gateway = productGatewayURL();
	const backend = new OnboardingBackend({
		connectBaseUrl: gateway,
		restBaseUrl: gateway,
		trustedGatewayHeaders: stampedGatewayHeaders(),
	});
	await backend.authenticateFixture("dev-admin");
	return backend;
}

describe("product gateway trust boundary", () => {
	test("rejects a product API call that carries no bearer token", async () => {
		const response = await connectRequest(
			productGatewayURL(),
			CREATE_ORGANIZATION,
			{ name: "Unauthenticated", slug: `unauth-${uniqueSuffix()}` },
			stampedGatewayHeaders(),
		);

		// Trust headers alone must never stand in for a user identity.
		expect(response.ok).toBe(false);
		expect([401, 403]).toContain(response.status);
	});

	test("does not honor caller-supplied trust headers", async () => {
		// A caller forging the internal token must not be able to talk its way
		// past the gateway. Only the frontend origin may present it, and it is
		// stamped server-side from Codefly's secret configuration.
		const response = await connectRequest(
			productGatewayURL(),
			CREATE_ORGANIZATION,
			{ name: "Forged", slug: `forged-${uniqueSuffix()}` },
			{
				[INTERNAL_TOKEN_HEADER]: "forged-internal-token",
				[PUBLIC_ORIGIN_HEADER]: "https://attacker.example.com",
			},
		);

		expect(response.ok).toBe(false);
		expect([401, 403]).toContain(response.status);
	});

	test("authenticates through the gateway when the proxy stamped the request", async () => {
		// The positive control for the two tests above: identical route, and the
		// only difference is that the trust headers are genuine.
		const response = await connectRequest(
			productGatewayURL(),
			AUTHENTICATE,
			{
				provider: "email",
				deviceInfo: "trust-boundary-pipeline-test",
				fixture: { token: "dev-admin" },
			},
			stampedGatewayHeaders(),
		);

		expect(response.status).toBe(200);
		const body = (await response.json()) as { accessToken?: string };
		expect(body.accessToken).toBeTruthy();
	});

	test("refuses to create an invitation when no verified public origin reaches Accounts", async () => {
		// Invitations embed an accept link, so Accounts needs the verified origin.
		// Authenticate with a stamped request, then drop only the public-origin
		// header: the user is still authentic, but the origin is gone.
		const gateway = productGatewayURL();
		const trusted = stampedGatewayHeaders();
		const withoutOrigin = {
			[INTERNAL_TOKEN_HEADER]: trusted[INTERNAL_TOKEN_HEADER],
		};

		const backend = await authenticatedBackend();
		const suffix = uniqueSuffix();
		const organizationId = await backend.createOrganization(
			`Origin Guard ${suffix}`,
			`origin-guard-${suffix}`,
		);
		await backend.switchOrganization(organizationId);

		const response = await connectRequest(
			gateway,
			"/saas.accounts.v1.InvitationService/CreateInvitation",
			{
				orgId: organizationId,
				email: `invite-${suffix}@example.com`,
				role: "INVITATION_ROLE_MEMBER",
			},
			{
				...withoutOrigin,
				Authorization: `Bearer ${backend.token()}`,
			},
		);

		// Fail closed: no verified origin means no invitation, rather than a link
		// built from a guessed or attacker-supplied host.
		expect(response.ok).toBe(false);
		const text = await response.text();
		expect(text).toMatch(/public application origin is unavailable/i);
	});
});
