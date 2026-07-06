import { useMutation, useQueryClient } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export function useGrantPlatformRole() {
  const svc = usePlatformAdminService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      platformRole,
    }: {
      userId: string;
      platformRole: string;
    }) => svc.grantPlatformRole({ userId, platformRole }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useRevokePlatformRole() {
  const svc = usePlatformAdminService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) => svc.revokePlatformRole({ userId }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useUpsertFeatureFlag() {
  const svc = usePlatformAdminService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (flag: {
      name: string;
      description?: string;
      enabled: boolean;
      rolloutPercent?: number;
      targetOrgIds?: string[];
    }) => svc.upsertFeatureFlag(flag),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["feature-flags"] }),
  });
}

export function useOverrideEntitlement() {
  const svc = usePlatformAdminService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      orgId,
      feature,
      limitValue,
      reason,
    }: {
      orgId: string;
      feature: string;
      limitValue: bigint;
      reason?: string;
    }) =>
      svc.overrideEntitlement({
        orgId,
        feature,
        limitValue,
        reason: reason ?? "",
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["entitlements"] }),
  });
}

export function useImpersonateUser() {
  const svc = usePlatformAdminService();
  return useMutation({
    mutationFn: (userId: string) => svc.impersonateUser({ userId }),
  });
}

export function useRevokeSession() {
  const svc = usePlatformAdminService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, reason }: { sessionId: string; reason?: string }) =>
      svc.revokeSession({ sessionId, reason: reason ?? "" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
}
