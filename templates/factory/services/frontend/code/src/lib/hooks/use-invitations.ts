import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function useInvitations(orgId: string | null) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["invitations", orgId],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/invitations", {
        params: { query: { org_id: orgId! } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!orgId,
    select: (data) => data?.invitations ?? [],
  });
}

export function useCreateInvitation() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ orgId, email, role }: { orgId: string; email: string; role: string }) => {
      const { data, error } = await client.POST("/v1/invitations", {
        body: { org_id: orgId, email, role },
      });
      if (error) throw error;
      return data;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["invitations"] }),
  });
}

export function useRevokeInvitation() {
  const client = useAPIClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await client.DELETE("/v1/invitations/{id}", {
        params: { path: { id } },
      });
      if (error) throw error;
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["invitations"] }),
  });
}
