import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { usePlatformAdminService } from "./use-api-client";

export function usePlatformAdmins() {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["platform-admins"],
    queryFn: () => svc.listPlatformAdmins({}),
    select: (data) => data.admins,
  });
}

export function useGrantPlatformRole() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, platformRole }: { userId: string; platformRole: string }) =>
      svc.grantPlatformRole({ userId, platformRole }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useRevokePlatformRole() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => svc.revokePlatformRole({ userId }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useImpersonateUser() {
  const svc = usePlatformAdminService();
  return useMutation({
    mutationFn: (userId: string) => svc.impersonateUser({ userId }),
  });
}

export function useOverrideEntitlement() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orgId, feature, limitValue, reason }: {
      orgId: string;
      feature: string;
      limitValue: bigint;
      reason?: string;
    }) => svc.overrideEntitlement({ orgId, feature, limitValue, reason: reason ?? "" }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["entitlements"] }),
  });
}

export function useFeatureFlags() {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["feature-flags"],
    queryFn: () => svc.listFeatureFlags({}),
    select: (data) => data.flags,
  });
}

export function useUpsertFeatureFlag() {
  const svc = usePlatformAdminService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (flag: {
      name: string;
      description?: string;
      enabled: boolean;
      rolloutPercent?: number;
      targetOrgIds?: string[];
    }) => svc.upsertFeatureFlag(flag),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["feature-flags"] }),
  });
}
