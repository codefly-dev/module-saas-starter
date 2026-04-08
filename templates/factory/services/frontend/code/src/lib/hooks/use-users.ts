import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function useUsers(query: string) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["users", query],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/platform/users", {
        params: { query: { query, page_size: 50 } },
      });
      if (error) throw error;
      return data;
    },
    select: (data) => data?.users ?? [],
  });
}

export function useUser(uuid: string) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["users", uuid],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/users/{uuid}", {
        params: { path: { uuid } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!uuid,
  });
}

export function useSuspendUser() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ userId, reason }: { userId: string; reason: string }) => {
      const { error } = await client.POST("/v1/platform/users/{user_id}:suspend", {
        params: { path: { user_id: userId } },
        body: { reason },
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["users"] }),
  });
}

export function useUnsuspendUser() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (userId: string) => {
      const { error } = await client.POST("/v1/platform/users/{user_id}:unsuspend", {
        params: { path: { user_id: userId } },
        body: {},
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["users"] }),
  });
}
