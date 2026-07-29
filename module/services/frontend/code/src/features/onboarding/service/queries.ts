import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { OnboardingService } from "@/gen/saas/accounts/v1/onboarding_pb";
import { apiTransport } from "@/lib/connect/transport";
import { transformOnboardingProgress } from "../model/transforms";

const client = createClient(OnboardingService, apiTransport);

export const onboardingQueries = {
	progress: (organizationId: string) =>
		queryOptions({
			queryKey: ["onboarding", "progress", organizationId],
			queryFn: () => client.getProgress({ organizationId }),
			select: transformOnboardingProgress,
		}),
};
