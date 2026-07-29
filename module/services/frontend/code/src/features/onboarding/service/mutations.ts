import type { OnboardingStepId } from "@/gen/saas/accounts/v1/onboarding_pb";
import { onboardingClient } from "./client";

export const onboardingMutations = {
	completeStep: (organizationId: string, stepId: OnboardingStepId) =>
		onboardingClient.completeStep(organizationId, stepId),

	skipStep: (
		organizationId: string,
		stepId: OnboardingStepId,
		reason: string,
	) => onboardingClient.skipStep(organizationId, stepId, reason),
};
