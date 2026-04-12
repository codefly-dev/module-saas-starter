import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useUserService } from "./use-api-client";
import { create } from "@bufbuild/protobuf";
import { UserIdentitySchema } from "@/gen/user_pb";

export function useUserIdentities(userUuid: string | null) {
  const svc = useUserService();
  return useQuery({
    queryKey: ["user-identities", userUuid],
    queryFn: () => svc.listUserIdentities({ userUuid: userUuid! }),
    enabled: !!userUuid,
    select: (data) => data.identities,
  });
}

export function useAddIdentity() {
  const svc = useUserService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userUuid, provider, providerId, providerEmail }: {
      userUuid: string;
      provider: string;
      providerId: string;
      providerEmail?: string;
    }) => svc.addIdentity({
      userUuid,
      identity: create(UserIdentitySchema, {
        uuid: "",
        userUuid,
        provider,
        providerId,
        providerEmail: providerEmail ?? "",
      }),
    }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["user-identities"] }),
  });
}
