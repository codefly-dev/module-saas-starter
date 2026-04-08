import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function useAPIKeys(orgId: string | null) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["api-keys", orgId],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/api-keys", {
        params: { query: { organization_id: orgId!, page_size: 100 } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!orgId,
    select: (data) => data?.keys ?? [],
  });
}

export function useRevokeAPIKey() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await client.DELETE("/v1/api-keys/{id}", {
        params: { path: { id } },
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["api-keys"] }),
  });
}
