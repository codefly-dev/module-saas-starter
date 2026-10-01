import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ConnectGitHubInput, DatasourceClient } from "./types.js";

export const sourcesKey = (orgId: string) => ["datasources", orgId] as const;

/**
 * How often the panel's lists are re-read while a sync is running: the source
 * row's status and last-sync columns move as it progresses.
 */
export const ACTIVE_LIST_POLL_MS = 5_000;
/**
 * How often they are re-read when nothing is syncing. Every change this panel
 * makes invalidates what it changed, and a sync enqueued from this page speeds
 * the list up through the sync watch, so the idle poll only exists to notice a
 * change made somewhere else — a grant from another tab, a source connected by
 * another administrator. A fixed 5 s poll on all three lists, kept running in
 * background tabs, was ~100 calls in a few idle minutes.
 *
 * None of these poll in a background tab (TanStack's default); a tab coming
 * back to the foreground re-reads anything stale on focus.
 */
export const IDLE_LIST_POLL_MS = 60_000;

const scopesKey = (orgId: string) => ["datasource-boundaries", orgId] as const;

/**
 * The viewer's readable scopes, re-read on focus (`staleTime` 0) and on the
 * caller's poll. The panel only labels rows with the answer, so by default it
 * polls at the idle cadence and not in a background tab. A gate that takes
 * content off screen on a revocation (`CollectionReadBoundary`) passes its own,
 * faster cadence; both share one query, so the panel adds no calls beside it.
 */
export function useAccessibleScopes(
	client: DatasourceClient,
	orgId: string,
	poll: { interval: number; inBackground: boolean } = {
		interval: IDLE_LIST_POLL_MS,
		inBackground: false,
	},
) {
	const listAccessibleScopes = client.listAccessibleScopes?.bind(client);
	return useQuery({
		queryKey: scopesKey(orgId),
		queryFn: () => listAccessibleScopes?.(orgId) ?? [],
		enabled: !!orgId && !!listAccessibleScopes,
		retry: false,
		staleTime: 0,
		refetchInterval: poll.interval,
		refetchIntervalInBackground: poll.inBackground,
	});
}

/** `syncActive`: whether any of the org's sources has a sync in flight (`useAnySourceSyncActive`). */
export function useListSources(
	client: DatasourceClient,
	orgId: string,
	syncActive = false,
) {
	return useQuery({
		queryKey: sourcesKey(orgId),
		queryFn: () => client.listSources(orgId),
		enabled: !!orgId,
		refetchInterval: syncActive ? ACTIVE_LIST_POLL_MS : IDLE_LIST_POLL_MS,
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
