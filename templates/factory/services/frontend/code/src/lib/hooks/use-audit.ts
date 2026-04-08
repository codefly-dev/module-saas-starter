import { useQuery } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export interface AuditLogParams {
  org_id?: string;
  action?: string;
  actor_id?: string;
  page_size?: number;
}

export function useAuditLog(params: AuditLogParams) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["audit-log", params],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/audit", {
        params: {
          query: {
            org_id: params.org_id,
            action: params.action,
            actor_id: params.actor_id,
            page_size: params.page_size ?? 50,
          },
        },
      });
      if (error) throw error;
      return data;
    },
    select: (data) => ({
      events: data?.events ?? [],
      totalCount: data?.total_count ?? 0,
    }),
  });
}
