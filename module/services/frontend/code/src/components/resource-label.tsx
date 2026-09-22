"use client";

import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@/lib/auth";
import {
	useOrganizationService,
	useTeamService,
} from "@/lib/hooks/use-api-client";
import { UserLabel } from "./user-label";

export function ResourceLabel({
	resource,
	id,
	orgId,
}: {
	resource: string;
	id: string;
	orgId?: string;
}) {
	const { organizationId } = useAuth();
	const tenant = orgId || organizationId;
	const organizations = useOrganizationService();
	const teams = useTeamService();
	const named = resource === "organization" || resource === "team";
	const result = useQuery({
		queryKey: ["resource-label", tenant, resource, id],
		queryFn: async () =>
			resource === "organization"
				? organizations.getOrganization({ id })
				: ((await teams.listTeams({ orgId: tenant || "" })).teams.find(
						(team) => team.id === id,
					) ?? null),
		enabled: named && !!id && (resource === "organization" || !!tenant),
		staleTime: 60_000,
		retry: false,
	});
	if (resource === "user") return <UserLabel userId={id} />;
	const label = resource.replaceAll("_", " ");
	return (
		<span>
			{result.data?.name ||
				(label
					? label.charAt(0).toUpperCase() + label.slice(1)
					: "Resource unavailable")}
		</span>
	);
}
