import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ConnectGitHubInput, DatasourceClient } from "./types.js";

const sourcesKey = (orgId: string) => ["datasources", orgId] as const;

const scopesKey = (orgId: string) =>
	["datasource-boundaries", orgId] as const;

/**
 * The org's data boundaries the caller may act on. Stays disabled when the
 * client cannot reach the accessible-scopes RPC, and never retries: an
 * unresolved boundary degrades to its id, so a failed lookup must not turn into
 * a failed panel.
 *
 * Resolving one boundary costs a walk of every scope node the caller can reach
 * in the org — a grant at the org root covers the whole subtree, placed records
 * included — so this is far too expensive to repeat on every mount and window
 * focus. Grants change rarely and an added source invalidates the key
 * explicitly, so the answer is held rather than refetched on sight.
 */
export function useAccessibleScopes(client: DatasourceClient, orgId: string) {
	const listAccessibleScopes = client.listAccessibleScopes?.bind(client);
	return useQuery({
		queryKey: scopesKey(orgId),
		queryFn: () => listAccessibleScopes?.(orgId) ?? [],
		enabled: !!orgId && !!listAccessibleScopes,
		retry: false,
		staleTime: 5 * 60 * 1000,
	});
}

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
		// Connecting a source resolves its target collection to a boundary node —
		// reusing the org's existing node for that label, or minting one — so the
		// held boundary answer is stale the moment this succeeds. Without this the
		// new row renders an opaque id for a boundary the caller may well hold.
		onSuccess: (_result, input) => {
			queryClient.invalidateQueries({ queryKey: sourcesKey(input.orgId) });
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
