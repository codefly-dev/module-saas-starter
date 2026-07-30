import { createClient } from "@connectrpc/connect";
import {
	OnboardingService,
	type OnboardingStepId,
} from "@/gen/saas/accounts/v1/onboarding_pb";
import { apiTransport } from "@/lib/connect/transport";
import { transformOnboardingProgress } from "../model/transforms";

const client = createClient(OnboardingService, apiTransport);

export const onboardingClient = {
	async getProgress(organizationId: string) {
		return transformOnboardingProgress(
			await client.getProgress({ organizationId }),
		);
	},
	async completeStep(organizationId: string, stepId: OnboardingStepId) {
		return transformOnboardingProgress(
			await client.completeStep({ organizationId, stepId }),
		);
	},
	async skipStep(
		organizationId: string,
		stepId: OnboardingStepId,
		reason = "not_now",
	) {
		return transformOnboardingProgress(
			await client.skipStep({ organizationId, stepId, reason }),
		);
	},
};
