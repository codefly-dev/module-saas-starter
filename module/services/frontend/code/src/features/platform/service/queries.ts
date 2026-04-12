import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export function usePlatformAdmins() {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["platform-admins"],
    queryFn: () => svc.listPlatformAdmins({}),
    select: (data) => data.admins,
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

export function useActiveSessions(userId?: string) {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["sessions", userId],
    queryFn: () =>
      svc.listActiveSessions({ userId: userId ?? "", pageSize: 100 }),
    select: (data) => data.sessions,
  });
}

export function useOrgEntitlements(orgId: string | null) {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["entitlements", orgId],
    queryFn: () => svc.getOrgEntitlements({ orgId: orgId! }),
    enabled: !!orgId,
    select: (data) => ({
      planName: data.planName,
      entitlements: data.entitlements,
    }),
  });
}
