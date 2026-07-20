import { createClient } from "@connectrpc/connect";
import { OnboardingService } from "@/gen/saas/accounts/v1/onboarding_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(OnboardingService, apiTransport);

export const onboardingMutations = {
	completeStep: (stepName: string) => client.completeStep({ stepName }),

	skipStep: (stepName: string) => client.skipStep({ stepName }),
};
