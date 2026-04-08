import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function usePlatformAdmins() {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["platform-admins"],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/platform/admins");
      if (error) throw error;
      return data;
    },
    select: (data) => data?.admins ?? [],
  });
}

export function useGrantPlatformRole() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ userId, platformRole }: { userId: string; platformRole: string }) => {
      const { error } = await client.POST("/v1/platform/admins", {
        body: { user_id: userId, platform_role: platformRole },
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useRevokePlatformRole() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (userId: string) => {
      const { error } = await client.DELETE("/v1/platform/admins/{user_id}", {
        params: { path: { user_id: userId } },
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["platform-admins"] }),
  });
}

export function useFeatureFlags() {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["feature-flags"],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/platform/feature-flags");
      if (error) throw error;
      return data;
    },
    select: (data) => data?.flags ?? [],
  });
}

export function useUpsertFeatureFlag() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (flag: {
      name: string;
      description?: string;
      enabled: boolean;
      rolloutPercent?: number;
      targetOrgIds?: string[];
    }) => {
      const { error } = await client.PUT("/v1/platform/feature-flags/{name}", {
        params: { path: { name: flag.name } },
        body: flag,
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["feature-flags"] }),
  });
}
