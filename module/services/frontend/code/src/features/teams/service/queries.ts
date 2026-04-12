import { queryOptions } from "@tanstack/react-query";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import { TeamService } from "@/gen/user_pb";

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
