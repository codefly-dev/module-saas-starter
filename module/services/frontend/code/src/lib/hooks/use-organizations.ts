import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
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

export function useCreateOrganization() {
  const svc = useOrganizationService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => svc.createOrganization({ name }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["organizations"] }),
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

export function useAddOrgMember() {
  const svc = useOrganizationService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orgId, userId, role }: { orgId: string; userId: string; role?: number }) =>
      svc.addMember({ orgId, userId, role: role ?? 1 }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["org-members"] }),
  });
}

export function useRemoveOrgMember() {
  const svc = useOrganizationService();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orgId, userId }: { orgId: string; userId: string }) =>
      svc.removeMember({ orgId, userId }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["org-members"] }),
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
