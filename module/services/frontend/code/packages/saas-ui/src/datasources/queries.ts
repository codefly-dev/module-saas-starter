import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ConnectGitHubInput, DatasourceClient } from "./types.js";

const sourcesKey = (orgId: string) => ["datasources", orgId] as const;

export function useListSources(client: DatasourceClient, orgId: string) {
	return useQuery({
		queryKey: sourcesKey(orgId),
		queryFn: () => client.listSources(orgId),
		enabled: !!orgId,
	});
}

export function useAddGitHubSource(client: DatasourceClient) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (input: ConnectGitHubInput) => client.addGitHubSource(input),
		onSuccess: (_result, input) =>
			queryClient.invalidateQueries({ queryKey: sourcesKey(input.orgId) }),
	});
}

export function useSyncSource(client: DatasourceClient) {
	return useMutation({
		mutationFn: (input: { orgId: string; id: string }) =>
			client.syncSource(input.orgId, input.id),
	});
}

export function useDeleteSource(client: DatasourceClient) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (input: { orgId: string; id: string }) =>
			client.deleteSource(input.orgId, input.id),
		onSuccess: (_result, input) =>
			queryClient.invalidateQueries({ queryKey: sourcesKey(input.orgId) }),
	});
}
