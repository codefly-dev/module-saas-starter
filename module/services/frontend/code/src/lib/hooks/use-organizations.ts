import { useQuery } from "@tanstack/react-query";
import { useOrganizationService, usePlatformAdminService } from "./use-api-client";

export function useOrganizations() {
  const svc = useOrganizationService();
  return useQuery({
    queryKey: ["organizations"],
    queryFn: () => svc.listOrganizations({}),
    select: (data) => data.organizations,
  });
}

export function useOrganization(id: string) {
  const svc = useOrganizationService();
  return useQuery({
    queryKey: ["organizations", id],
    queryFn: () => svc.getOrganization({ id }),
    enabled: !!id,
  });
}

export function useOrgMembers(orgId: string | null) {
  const svc = useOrganizationService();
  return useQuery({
    queryKey: ["org-members", orgId],
    queryFn: () => svc.listMembers({ orgId: orgId! }),
    enabled: !!orgId,
    select: (data) => data.members,
  });
}

export function useOrgEntitlements(orgId: string | null) {
  const svc = usePlatformAdminService();
  return useQuery({
    queryKey: ["entitlements", orgId],
    queryFn: () => svc.getOrgEntitlements({ orgId: orgId! }),
    enabled: !!orgId,
  });
}
