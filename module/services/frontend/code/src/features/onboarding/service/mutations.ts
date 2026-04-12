// TODO: Replace with real Connect RPC client when OnboardingService is generated
// import { createClient } from "@connectrpc/connect";
// import { apiTransport } from "@/lib/connect/transport";
// import { OnboardingService } from "@/gen/user_pb";
// const client = createClient(OnboardingService, apiTransport);

export const onboardingMutations = {
  completeStep: async (stepId: string) => {
    // TODO: Replace with real RPC call
    // return client.completeStep({ stepId });
    return { stepId };
  },

  skipStep: async (stepId: string) => {
    // TODO: Replace with real RPC call
    // return client.skipStep({ stepId });
    return { stepId };
  },
};
