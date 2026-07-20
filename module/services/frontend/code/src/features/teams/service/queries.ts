import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { TeamService } from "@/gen/saas/accounts/v1/teams_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(TeamService, apiTransport);

export const teamQueries = {
	list: (orgId: string) =>
		queryOptions({
			queryKey: ["teams", orgId],
			queryFn: () => client.listTeams({ orgId }),
			enabled: !!orgId,
		}),

	members: (teamId: string) =>
		queryOptions({
			queryKey: ["team-members", teamId],
			queryFn: () => client.listMembers({ teamId }),
			enabled: !!teamId,
		}),
};
