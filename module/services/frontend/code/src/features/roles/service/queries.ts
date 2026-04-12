import { useQuery } from "@tanstack/react-query";
import { usePermissionService } from "@/lib/hooks/use-api-client";

export function useRoles() {
  const svc = usePermissionService();
  return useQuery({
    queryKey: ["roles"],
    queryFn: () => svc.listRoles({}),
    select: (data) => data.roles,
  });
}
