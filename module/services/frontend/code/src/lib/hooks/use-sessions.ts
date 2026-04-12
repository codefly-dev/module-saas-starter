import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "./use-api-client";

export function useActiveSessions(userId?: string) {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["sessions", userId],
    queryFn: () => svc.listActiveSessions({ userId: userId ?? "", pageSize: 100 }),
    select: (data) => data.sessions,
  });
}
