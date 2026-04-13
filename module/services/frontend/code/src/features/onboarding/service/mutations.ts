import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { OnboardingService } from "@/gen/user_pb";

const client = createClient(OnboardingService, apiTransport);

export const onboardingMutations = {
  completeStep: (stepName: string) =>
    client.completeStep({ stepName }),

  skipStep: (stepName: string) =>
    client.skipStep({ stepName }),
};
