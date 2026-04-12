import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIKeyService } from "@/lib/hooks/use-api-client";

export function useCreateAPIKey() {
  const svc = useAPIKeyService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      organizationId,
      name,
      scopes,
      environment,
    }: {
      organizationId: string;
      name: string;
      scopes?: { resource: string; action: string }[];
      environment?: number;
    }) =>
      svc.createAPIKey({
        organizationId,
        name,
        scopes: scopes ?? [],
        environment: environment ?? 1,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
  });
}

export function useRevokeAPIKey() {
  const svc = useAPIKeyService();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => svc.revokeAPIKey({ id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
  });
}
