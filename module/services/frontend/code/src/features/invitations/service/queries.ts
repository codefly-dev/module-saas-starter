import { useQuery } from "@tanstack/react-query";
import { useInvitationService } from "@/lib/hooks/use-api-client";

export function useInvitations(orgId: string | null) {
  const svc = useInvitationService();
  return useQuery({
    queryKey: ["invitations", orgId],
    queryFn: () => svc.listInvitations({ orgId: orgId! }),
    enabled: !!orgId,
    select: (data) => data.invitations,
  });
}
