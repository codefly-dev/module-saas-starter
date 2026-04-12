import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useUserService, usePlatformAdminService } from "./use-api-client";

export function useUsers(query: string) {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["users", query],
    queryFn: () => svc.searchUsers({ query, pageSize: 50 }),
    select: (data) => data.users,
  });
}

export function useUser(uuid: string) {
  const svc = useUserService();
  return useQuery({
    queryKey: ["users", uuid],
    queryFn: () => svc.getUser({ identifier: { case: "uuid", value: uuid } }),
    enabled: !!uuid,
  });
}

export function useSuspendUser() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, reason }: { userId: string; reason: string }) =>
      svc.suspendUser({ userId, reason }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["users"] }),
  });
}

export function useUnsuspendUser() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => svc.unsuspendUser({ userId }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["users"] }),
  });
}
