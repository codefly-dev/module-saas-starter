import { createClient } from "@connectrpc/connect";
import {
	OnboardingService,
	type OnboardingStepId,
} from "@/gen/saas/accounts/v1/onboarding_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(OnboardingService, apiTransport);

export const onboardingMutations = {
	completeStep: (organizationId: string, stepId: OnboardingStepId) =>
		client.completeStep({ organizationId, stepId }),

	skipStep: (
		organizationId: string,
		stepId: OnboardingStepId,
		reason: string,
	) => client.skipStep({ organizationId, stepId, reason }),
};
