import { useQuery } from "@tanstack/react-query";
import { useOrganizationService } from "./use-api-client";

export function useOrganizations() {
	const svc = useOrganizationService();
	return useQuery({
		queryKey: ["organizations"],
		queryFn: () => svc.listOrganizations({}),
		select: (data) => data.organizations,
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
