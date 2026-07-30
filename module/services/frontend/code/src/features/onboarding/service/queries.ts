import { queryOptions } from "@tanstack/react-query";
import { onboardingClient } from "./client";

export const onboardingQueries = {
	progress: (organizationId: string) =>
		queryOptions({
			queryKey: ["onboarding", "progress", organizationId],
			queryFn: () => onboardingClient.getProgress(organizationId),
		}),
};
