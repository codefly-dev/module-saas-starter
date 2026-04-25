import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useAPIKeyService } from "./use-api-client";

export function useAPIKeys(orgId: string | null) {
  const svc = useAPIKeyService();
  return useQuery({
    queryKey: ["api-keys", orgId],
    queryFn: () => svc.listAPIKeys({ organizationId: orgId!, pageSize: 100 }),
    enabled: !!orgId,
    select: (data) => data.keys,
  });
}

export function useCreateAPIKey() {
  const svc = useAPIKeyService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ organizationId, name, scopes, environment }: {
      organizationId: string;
      name: string;
      scopes?: { resource: string; action: string }[];
      environment?: number;
    }) => svc.createAPIKey({ organizationId, name, scopes: scopes ?? [], environment: environment ?? 1 }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      toast.success("API key created", {
        description: "Copy the secret now — it won't be shown again.",
      });
    },
    onError: (err) =>
      toast.error("Couldn't create API key", { description: err.message }),
  });
}

export function useRevokeAPIKey() {
  const svc = useAPIKeyService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => svc.revokeAPIKey({ id }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["api-keys"] });
      toast.success("API key revoked");
    },
    onError: (err) =>
      toast.error("Couldn't revoke API key", { description: err.message }),
  });
}
