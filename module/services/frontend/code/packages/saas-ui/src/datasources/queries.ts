import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ConnectGitHubInput, DatasourceClient } from "./types.js";

const sourcesKey = (orgId: string) => ["datasources", orgId] as const;

const scopesKey = (orgId: string) => ["datasource-boundaries", orgId] as const;

export function useAccessibleScopes(client: DatasourceClient, orgId: string) {
	const listAccessibleScopes = client.listAccessibleScopes?.bind(client);
	return useQuery({
		queryKey: scopesKey(orgId),
		queryFn: () => listAccessibleScopes?.(orgId) ?? [],
		enabled: !!orgId && !!listAccessibleScopes,
		retry: false,
		staleTime: 0,
		refetchInterval: 5000,
		refetchIntervalInBackground: true,
	});
}

export function useListSources(client: DatasourceClient, orgId: string) {
	return useQuery({
		queryKey: sourcesKey(orgId),
		queryFn: () => client.listSources(orgId),
		enabled: !!orgId,
		refetchInterval: 5000,
	});
}

export function useAddGitHubSource(client: DatasourceClient) {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (input: ConnectGitHubInput) => client.addGitHubSource(input),
		// Connecting a source resolves its target collection to a boundary node —
		// reusing the org's existing node for that label, or minting one — so the
		// held boundary answer is stale the moment this succeeds. Without this the
		// new row renders an opaque id for a boundary the caller may well hold.
		onSuccess: (_result, input) => {
			queryClient.invalidateQueries({ queryKey: sourcesKey(input.orgId) });
			queryClient.invalidateQueries({
				queryKey: ["collection-access", input.orgId],
			});
			queryClient.invalidateQueries({ queryKey: scopesKey(input.orgId) });
		},
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
