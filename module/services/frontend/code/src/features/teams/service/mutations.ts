import { createClient } from "@connectrpc/connect";
import { TeamService } from "@/gen/saas/accounts/v1/teams_pb";
import { apiTransport } from "@/lib/connect/transport";

const client = createClient(TeamService, apiTransport);

export const teamMutations = {
	create: (orgId: string, name: string, description?: string) =>
		client.createTeam({ orgId, name, description: description ?? "" }),

	addMember: (teamId: string, userId: string, role: number) =>
		client.addMember({ teamId, userId, role }),

	removeMember: (teamId: string, userId: string) =>
		client.removeMember({ teamId, userId }),

	update: (teamId: string, name: string, description?: string) =>
		client.updateTeam({ teamId, name, description: description ?? "" }),

	remove: (teamId: string) => client.deleteTeam({ teamId }),
};
