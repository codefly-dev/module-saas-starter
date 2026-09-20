import { createClient } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { TeamService } from "@/gen/saas/accounts/v1/teams_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(TeamService, apiTransport);

export const teamQueries = {
	// memberId narrows to the teams that principal belongs to. It is part of
	// the key so a member's teams and the org's teams are distinct entries
	// rather than one overwriting the other.
	list: (orgId: string, memberId = "") =>
		queryOptions({
			queryKey: ["teams", orgId, memberId],
			queryFn: () => client.listTeams({ orgId, memberId }),
			enabled: !!orgId,
		}),

	members: (teamId: string) =>
		queryOptions({
			queryKey: ["team-members", teamId],
			queryFn: () => client.listMembers({ teamId }),
			enabled: !!teamId,
		}),
};
