import { useQuery } from "@tanstack/react-query";
import { useAPIClient } from "./use-api-client";

export function useOrganizations() {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/organizations");
      if (error) throw error;
      return data;
    },
    select: (data) => data?.organizations ?? [],
  });
}

export function useOrganization(id: string) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["organizations", id],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!id,
  });
}

export function useOrgMembers(orgId: string | null) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["org-members", orgId],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/organizations/{org_id}/members", {
        params: { path: { org_id: orgId! } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!orgId,
    select: (data) => data?.members ?? [],
  });
}

export function useOrgEntitlements(orgId: string | null) {
  const client = useAPIClient();
  return useQuery({
    queryKey: ["entitlements", orgId],
    queryFn: async () => {
      const { data, error } = await client.GET("/v1/platform/organizations/{org_id}/entitlements", {
        params: { path: { org_id: orgId! } },
      });
      if (error) throw error;
      return data;
    },
    enabled: !!orgId,
  });
}
