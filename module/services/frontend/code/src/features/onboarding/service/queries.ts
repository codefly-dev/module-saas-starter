import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { OnboardingService } from "@/gen/saas/accounts/v1/onboarding_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(OnboardingService, apiTransport);

export const onboardingQueries = {
	progress: () =>
		queryOptions({
			queryKey: ["onboarding", "progress"],
			queryFn: () => client.getProgress({}),
		}),
};
