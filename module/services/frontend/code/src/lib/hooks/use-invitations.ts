import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useInvitationService } from "./use-api-client";

export function useInvitations(orgId: string | null) {
  const svc = useInvitationService();
  return useQuery({
    queryKey: ["invitations", orgId],
    queryFn: () => svc.listInvitations({ orgId: orgId! }),
    enabled: !!orgId,
    select: (data) => data.invitations,
  });
}

export function useCreateInvitation() {
  const svc = useInvitationService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orgId, email, role }: { orgId: string; email: string; role: string }) =>
      svc.createInvitation({ orgId, email, role }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["invitations"] }),
  });
}

export function useAcceptInvitation() {
  const svc = useInvitationService();
  return useMutation({
    mutationFn: (token: string) => svc.acceptInvitation({ token }),
  });
}

export function useRevokeInvitation() {
  const svc = useInvitationService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => svc.revokeInvitation({ id }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["invitations"] }),
  });
}
