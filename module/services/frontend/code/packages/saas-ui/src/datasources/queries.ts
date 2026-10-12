import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import {
	type AdmittedReconcilePass,
	createReconcilePassLedger,
	notifySourceReconcileAdmitted,
} from "./reconcile-passes.js";
import type {
	ConnectGitHubInput,
	DatasourceClient,
	SourceReconcileReceipt,
} from "./types.js";

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

/**
 * "Sync now", as the admission it is: one occurrence of
 * `DatasourceService/ReconcileSource` under an effect identity this press
 * minted, indistinguishable in the host's receipts from a declared schedule's
 * occurrence.
 *
 * The three steps it owns, and why each is here rather than in a component:
 *
 * 1. **Present an identity.** A ledger held for this mounted surface mints one
 *    per pass and keeps it against the source, so a repeat press names the
 *    same pass.
 * 2. **Recover before re-admitting.** A repeat press reads the receipt back
 *    through `LookupReconcileSource` and surfaces the ORIGINAL — one receipt
 *    for two presses, and no second re-send of the source. A lookup that comes
 *    back "unknown" is inconclusive, so the same identity is re-presented to
 *    the operation rather than a new one minted.
 * 3. **Announce the pass.** The host's reconcile-schedules panel shows it
 *    beside the declared schedule's occurrences, which is only possible if the
 *    effect identity leaves this call.
 */
export function useReconcileSource(client: DatasourceClient) {
	// One ledger per mounted surface: see `ReconcilePassLedger` for why that is
	// the lifetime rather than the module's. Held in state's lazy initializer
	// rather than a ref written during render, which the compiler refuses.
	const [passes] = useState(createReconcilePassLedger);
	return useMutation({
		mutationFn: async (input: {
			orgId: string;
			id: string;
		}): Promise<AdmittedReconcilePass> => {
			const { effectId, repeat } = passes.present(input.id);
			if (repeat) {
				const prior = await client.lookupReconcileSource(effectId);
				if (prior && prior.status === "committed") {
					// The person has now been told the pass is already recorded, so
					// the next press is a request they mean rather than a slip.
					passes.retire(input.id);
					return admitted(input.id, effectId, prior, true);
				}
			}
			const receipt = await client.reconcileSource(
				input.orgId,
				input.id,
				effectId,
			);
			return admitted(input.id, effectId, receipt, false);
		},
	});
}

/**
 * The pass, announced and returned. The identity is the one this call
 * PRESENTED rather than the one the response echoed: the pass is named by what
 * the caller committed under, and a host that echoed none back would otherwise
 * leave it nameless — which the registry drops.
 */
function admitted(
	sourceId: string,
	effectId: string,
	receipt: SourceReconcileReceipt,
	repeat: boolean,
): AdmittedReconcilePass {
	const pass: AdmittedReconcilePass = {
		sourceId,
		effectId: receipt.effectId || effectId,
		jobId: receipt.jobId,
		status: receipt.status,
		repeat,
		at: new Date().toISOString(),
	};
	notifySourceReconcileAdmitted(pass);
	return pass;
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
