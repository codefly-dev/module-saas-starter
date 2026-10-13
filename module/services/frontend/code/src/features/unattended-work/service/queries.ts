import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useDatasourceService } from "@/lib/hooks/use-api-client";
import { mayKeepRetainedRows, readOutcome } from "@/shared/lib/read-outcome";

/**
 * The reads behind the unattended-work playground.
 *
 * Nothing here decides anything. A trigger's declaration, the references an
 * operator may bind to it and the children minted against them are all the
 * host's answers, re-read on demand: a capability lives about a minute and is
 * never cached, so the only durable record of one is the trail this read
 * projects.
 */
export const unattendedWorkKeys = {
	sources: (orgId: string) => ["unattended-sources", orgId] as const,
	declaration: (orgId: string, sourceId: string) =>
		["source-trigger-declaration", orgId, sourceId] as const,
};

/** The organization's connected sources, which are what declares a trigger. */
export function useTriggerSources(orgId: string) {
	const service = useDatasourceService();
	return useQuery({
		queryKey: unattendedWorkKeys.sources(orgId),
		enabled: !!orgId,
		queryFn: async () => (await service.listSources({ orgId })).datasources,
	});
}

/**
 * One source's declared reconcile trigger, the references bound to it, and the
 * children minted against them.
 */
export function useSourceTriggerDeclaration(orgId: string, sourceId: string) {
	const service = useDatasourceService();
	const client = useQueryClient();
	return useQuery({
		queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
		enabled: !!orgId && !!sourceId,
		queryFn: async () => {
			// WHICH GENERATION OF THE ANSWER THIS FETCH IS READING. A refusal
			// may only delete the answer it was refused about; if a newer fetch
			// has written one since, this fetch's refusal is stale news (issue
			// #1121).
			//
			// The field is `dataUpdateCount`, the lifecycle's own counter, and
			// NOT `dataUpdatedAt`: that is a millisecond timestamp, so two
			// successes in the same millisecond tie and the guard reads a newer
			// answer as the one it was reading — at exactly the sub-millisecond
			// spacing an overlapping refetch has. A generation cannot tie.
			const readingGeneration =
				client.getQueryCache().find({
					queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
					exact: true,
				})?.state.dataUpdateCount ?? 0;
			try {
				return await service.getSourceTriggerDeclaration({ orgId, sourceId });
			} catch (error) {
				// A REFUSAL DELETES WHAT IT WAS READING, and it does it here
				// rather than at render (issue #1121). TanStack keeps the last
				// successful answer when a refetch rejects, so a viewer whose
				// authority was withdrawn went on seeing the declaration — and
				// the sweep row beside it — from cache. Authority is a property
				// of the read, so what the read may leave behind is too.
				//
				// Only a refusal clears. A transient failure keeps the prior
				// answer under a stale notice, because blanking a good answer
				// because one request did not come back is worse than the
				// staleness (shared/lib/read-outcome.ts).
				//
				// AND IT CLEARS EXACTLY ONE KEY — the one that was refused.
				// `findAll({ queryKey })` matches by PREFIX, so clearing
				// `["source-trigger-declaration"]` reached every organization
				// and every source: one source's 403 blanked a sibling source
				// whose own read had succeeded. That sibling's query stays in
				// `success` with no error, so its row has no refusal to report
				// and says "This source declares no trigger" — a false
				// statement about a source the viewer may see. A refusal is
				// evidence about the key it answered, and about no other.
				//
				// AND ONLY WHILE IT IS STILL THIS FETCH'S ANSWER TO DELETE. Two
				// fetches overlap; the older is superseded, answers last and
				// answers 403, and deleting here erased the newer fetch's
				// success — leaving status `success`, fetchStatus `idle`, data
				// undefined and error null, a state no component reads
				// correctly: the row has no refusal to report and no
				// declaration to show, so it says a source declares no trigger
				// when its latest read succeeded. A superseded refusal is stale
				// news about authority, not current evidence. The deletion
				// still happens when nothing newer replaced the answer —
				// including with no component mounted, which is the case this
				// guard must not swallow.
				if (!mayKeepRetainedRows(readOutcome(true, error))) {
					const entry = client.getQueryCache().find({
						queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
						exact: true,
					});
					if (entry && entry.state.dataUpdateCount <= readingGeneration) {
						entry.setState({ data: undefined, dataUpdatedAt: 0 });
					}
				}
				throw error;
			}
		},
	});
}
