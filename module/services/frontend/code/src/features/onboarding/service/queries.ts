import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { OnboardingService } from "@/gen/user_pb";

const client = createClient(OnboardingService, apiTransport);

export const onboardingQueries = {
  progress: () =>
    queryOptions({
      queryKey: ["onboarding", "progress"],
      queryFn: () => client.getProgress({}),
    }),
};
