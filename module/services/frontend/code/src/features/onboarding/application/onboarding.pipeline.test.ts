import {
	getCurrentModule,
	getEndpoints,
	resolveServiceAddressSync,
} from "codefly";
import { describe, expect, test } from "vitest";
import { OnboardingStepStatus } from "../model/types";
import { OnboardingBackend } from "./backend";

function requiredEndpoint(type: "connect" | "rest"): string {
	const protocol = type.toUpperCase();
	const currentModule = getCurrentModule();
	const injected = getEndpoints().filter(
		(endpoint) =>
			endpoint.service === "accounts" &&
			endpoint.name === type &&
			endpoint.protocol === protocol &&
			(!currentModule || endpoint.module === currentModule),
	);
	if (injected.length === 1 && injected[0].address) {
		return injected[0].address;
	}
	if (injected.length > 1) {
		throw new Error(
			`Codefly injected multiple accounts/${type} endpoints into the frontend test runtime.`,
		);
	}

	// Supports running the file directly for local diagnostics. The normal
	// Codefly test path always uses the dependency endpoint injected above,
	// which remains correct under isolated naming scopes.
	const scope = process.env.CODEFLY__NAMING_SCOPE ?? "";
	const endpoint = resolveServiceAddressSync("accounts", type, {
		scope,
		cwd: process.cwd(),
	});
	if (!endpoint) {
		throw new Error(
			`Codefly did not resolve accounts/${type}; run this through ` +
				"`codefly test service --suite unit`.",
		);
	}
	return endpoint;
}

describe("headless frontend onboarding pipeline", () => {
	test("drives real organization, invitation, billing, API-key, events, and progress persistence", async () => {
		const suffix = crypto.randomUUID().replaceAll("-", "").slice(0, 12);
		const connectBaseUrl = requiredEndpoint("connect");
		const restBaseUrl = requiredEndpoint("rest");
		const backend = new OnboardingBackend({
			connectBaseUrl,
			restBaseUrl,
		});

		try {
			await backend.authenticateFixture("dev-admin");
		} catch (error) {
			throw new Error(
				`Cannot authenticate against Codefly Accounts endpoints ` +
					`connect=${connectBaseUrl} rest=${restBaseUrl}`,
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
