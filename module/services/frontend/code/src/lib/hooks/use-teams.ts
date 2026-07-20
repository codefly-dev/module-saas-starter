import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTeamService } from "./use-api-client";

export function useTeams(orgId: string | null) {
	const svc = useTeamService();
	return useQuery({
		queryKey: ["teams", orgId],
		queryFn: () => svc.listTeams({ orgId: orgId! }),
		enabled: !!orgId,
		select: (data) => data.teams,
	});
}

export function useCreateTeam() {
	const svc = useTeamService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({ orgId, name }: { orgId: string; name: string }) =>
			svc.createTeam({ orgId, name }),
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["teams"] }),
	});
}

export function useTeamMembers(teamId: string | null) {
	const svc = useTeamService();
	return useQuery({
		queryKey: ["team-members", teamId],
		queryFn: () => svc.listMembers({ teamId: teamId! }),
		enabled: !!teamId,
		select: (data) => data.members,
	});
}

export function useAddTeamMember() {
	const svc = useTeamService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({ teamId, userId }: { teamId: string; userId: string }) =>
			svc.addMember({ teamId, userId }),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["team-members"] }),
	});
}

export function useRemoveTeamMember() {
	const svc = useTeamService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({ teamId, userId }: { teamId: string; userId: string }) =>
			svc.removeMember({ teamId, userId }),
		onSuccess: () =>
			queryClient.invalidateQueries({ queryKey: ["team-members"] }),
	});
}
