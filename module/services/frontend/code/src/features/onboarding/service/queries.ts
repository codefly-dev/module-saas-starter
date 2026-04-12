import { queryOptions } from "@tanstack/react-query";

// TODO: Replace with real Connect RPC client when OnboardingService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { OnboardingService } from "@/gen/user_pb";
// const client = createClient(OnboardingService, apiTransport);

export const onboardingQueries = {
  progress: () =>
    queryOptions({
      queryKey: ["onboarding", "progress"],
      queryFn: async () => {
        // TODO: Replace with real RPC call
        // return client.getOnboardingProgress({});
        return {
          completedSteps: [] as string[],
          skippedSteps: [] as string[],
          currentStep: "create_org",
          startedAt: new Date().toISOString(),
        };
      },
    }),
};
