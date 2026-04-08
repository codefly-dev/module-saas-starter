import { useQuery } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function useActiveSessions(userId?: string) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["sessions", userId],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/platform/sessions", {
        params: { query: { user_id: userId, page_size: 100 } },
      });
      if (error) throw error;
      return data;
    },
    select: (data) => data?.sessions ?? [],
  });
}
