import { useQuery } from "@tanstack/react-query";
import { useAPIKeyService } from "@/lib/hooks/use-api-client";

export function useAPIKeys(orgId: string | null) {
  const svc = useAPIKeyService();
  return useQuery({
    queryKey: ["api-keys", orgId],
    queryFn: () => svc.listAPIKeys({ organizationId: orgId!, pageSize: 100 }),
    enabled: !!orgId,
    select: (data) => data.keys,
  });
}
