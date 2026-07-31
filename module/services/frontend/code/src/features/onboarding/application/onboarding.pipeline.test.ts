import { describe, expect, test } from "vitest";
import {
	productGatewayURL,
	stampedGatewayHeaders,
	uniqueSuffix,
} from "@/test/pipeline-gateway";
import { OnboardingStepStatus } from "../model/types";
import { OnboardingBackend } from "./backend";

describe("headless frontend onboarding pipeline", () => {
	test("drives real organization, invitation, billing, API-key, events, and progress persistence", async () => {
		const suffix = uniqueSuffix();
		const gatewayBaseURL = productGatewayURL();
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
