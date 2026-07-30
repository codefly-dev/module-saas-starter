import {
	createClient,
	type Interceptor,
	type Transport,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
	APIKeyEnvironment,
	APIKeyService,
} from "@/gen/saas/accounts/v1/api_keys_pb";
import { AuthService } from "@/gen/saas/accounts/v1/authentication_pb";
import {
	InvitationRole,
	InvitationService,
} from "@/gen/saas/accounts/v1/invitations_pb";
import {
	OnboardingService,
	type OnboardingStepId,
} from "@/gen/saas/accounts/v1/onboarding_pb";
import { OrganizationService } from "@/gen/saas/accounts/v1/organizations_pb";
import { transformOnboardingProgress } from "../model/transforms";
import type { OnboardingProgress } from "../model/types";

export interface CompleteOnboardingPipelineInput {
	organizationName: string;
	organizationSlug: string;
	inviteEmail: string;
	apiKeyName: string;
}

export interface CompleteOnboardingPipelineResult {
	organizationId: string;
	progress: OnboardingProgress;
}

export interface OnboardingBackendOptions {
	connectBaseUrl: string;
	restBaseUrl: string;
	accessToken?: string;
	fetch?: typeof globalThis.fetch;
	/**
	 * Server-to-server trust headers normally stamped by the Next proxy on
	 * same-origin product API requests. In the browser the frontend origin adds
	 * them, so the React controller never sets this. A headless caller that
	 * addresses the private gateway directly must supply the headers the proxy
	 * would have produced, otherwise the gateway cannot establish a verified
	 * public origin and origin-dependent flows fail closed.
	 */
	trustedGatewayHeaders?: Record<string, string>;
}

/**
 * Headless production client for the complete onboarding capability.
 *
 * It implements the same application boundary used by the React controller
 * and also exposes a complete pipeline driver. Tests point it at
 * Codefly-resolved Accounts endpoints, so an ordinary Vitest case crosses the
 * real RPC adapters, business service, PostgreSQL, Vault, durable jobs, and
 * product event ledger without a browser or mocked backend.
 */
export class OnboardingBackend {
	private accessToken: string;
	private readonly fetcher: typeof globalThis.fetch;
	private readonly restBaseUrl: string;
	private readonly trustedGatewayHeaders: Record<string, string>;
	private readonly auth;
	private readonly organizations;
	private readonly invitations;
	private readonly apiKeys;
	private readonly onboarding;

	constructor(options: OnboardingBackendOptions) {
		this.accessToken = options.accessToken ?? "";
		this.fetcher = options.fetch ?? globalThis.fetch;
		this.restBaseUrl = options.restBaseUrl.replace(/\/$/, "");
		this.trustedGatewayHeaders = { ...(options.trustedGatewayHeaders ?? {}) };

		const transport = authenticatedTransport(
			options.connectBaseUrl,
			() => this.accessToken,
			this.trustedGatewayHeaders,
		);
		this.auth = createClient(AuthService, transport);
		this.organizations = createClient(OrganizationService, transport);
		this.invitations = createClient(InvitationService, transport);
		this.apiKeys = createClient(APIKeyService, transport);
		this.onboarding = createClient(OnboardingService, transport);
	}

	token(): string {
		return this.accessToken;
	}

	setToken(accessToken: string): void {
		this.accessToken = accessToken;
	}

	async authenticateFixture(
		fixtureToken: string,
		provider = "email",
	): Promise<string> {
		const response = await this.auth.authenticate({
			provider,
			deviceInfo: "codefly-headless-frontend-library-test",
			authentication: {
				case: "fixture",
				value: { token: fixtureToken },
			},
		});
		if (!response.accessToken) {
			throw new Error("Fixture authentication returned no access token");
		}
		if (response.mfaRequired) {
			throw new Error("Fixture authentication unexpectedly requires MFA");
		}
		this.accessToken = response.accessToken;
		return response.accessToken;
	}

	async createOrganization(name: string, slug: string): Promise<string> {
		const response = await this.organizations.createOrganization({
			name,
			slug,
		});
		const organizationId = response.organization?.id;
		if (!organizationId) {
			throw new Error("Organization creation returned no organization");
		}
		return organizationId;
	}

	async switchOrganization(organizationId: string): Promise<string> {
		const response = await this.auth.switchOrganization({ organizationId });
		if (!response.accessToken) {
			throw new Error("Organization switch returned no access token");
		}
		this.accessToken = response.accessToken;
		return response.accessToken;
	}

	async getProgress(organizationId: string): Promise<OnboardingProgress> {
		return transformOnboardingProgress(
			await this.onboarding.getProgress({ organizationId }),
		);
	}

	async skipStep(
		organizationId: string,
		stepId: OnboardingStepId,
		reason = "not_now",
	): Promise<OnboardingProgress> {
		return transformOnboardingProgress(
			await this.onboarding.skipStep({ organizationId, stepId, reason }),
		);
	}

	async inviteTeamMember(organizationId: string, email: string): Promise<void> {
		await this.invitations.createInvitation({
			orgId: organizationId,
			email,
			role: InvitationRole.MEMBER,
		});
	}

	async selectFreePlan(): Promise<void> {
		const response = await this.fetcher(
			`${this.restBaseUrl}/v1/billing/free-plan`,
			{
				method: "POST",
				headers: {
					...this.trustedGatewayHeaders,
					Authorization: `Bearer ${this.requiredToken()}`,
					"Content-Type": "application/json",
					"Idempotency-Key": crypto.randomUUID(),
				},
			},
		);
		if (!response.ok) {
			throw new Error(
				`Free plan selection failed (${response.status}): ${await response.text()}`,
			);
		}
	}

	async createAPIKey(organizationId: string, name: string): Promise<void> {
		const response = await this.apiKeys.createAPIKey({
			organizationId,
			name,
			environment: APIKeyEnvironment.API_KEY_ENVIRONMENT_TEST,
			scopes: [],
		});
		if (!response.key || !response.plaintextKey) {
			throw new Error("API key creation did not return its one-time secret");
		}
		// The secret is deliberately not retained by the application library.
	}

	async completePipeline(
		input: CompleteOnboardingPipelineInput,
	): Promise<CompleteOnboardingPipelineResult> {
		const organizationId = await this.createOrganization(
			input.organizationName,
			input.organizationSlug,
		);
		await this.switchOrganization(organizationId);

		await this.getProgress(organizationId);
		await this.inviteTeamMember(organizationId, input.inviteEmail);
		await this.selectFreePlan();
		await this.createAPIKey(organizationId, input.apiKeyName);

		return {
			organizationId,
			progress: await this.getProgress(organizationId),
		};
	}

	private requiredToken(): string {
		if (!this.accessToken) {
			throw new Error("Authentication required");
		}
		return this.accessToken;
	}
}

function authenticatedTransport(
	baseUrl: string,
	token: () => string,
	trustedGatewayHeaders: Record<string, string> = {},
): Transport {
	const authentication: Interceptor = (next) => async (request) => {
		const accessToken = token();
		if (accessToken) {
			request.header.set("Authorization", `Bearer ${accessToken}`);
		}
		for (const [name, value] of Object.entries(trustedGatewayHeaders)) {
			request.header.set(name, value);
		}
		return next(request);
	};
	return createConnectTransport({
		baseUrl: baseUrl.replace(/\/$/, ""),
		interceptors: [authentication],
	});
}
