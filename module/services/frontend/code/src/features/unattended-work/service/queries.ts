import { useQuery } from "@tanstack/react-query";
import { useDatasourceService } from "@/lib/hooks/use-api-client";

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
	return useQuery({
		queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
		enabled: !!orgId && !!sourceId,
		queryFn: () => service.getSourceTriggerDeclaration({ orgId, sourceId }),
	});
}
