import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export const moduleEnvelopeKey = ["module-envelope"] as const;

/**
 * The module envelope and what the applied module manifests ask of it, in one
 * read: whether a principal is allowed is answered against the revision this
 * same response carries, never against one read at another moment.
 */
export function useModuleEnvelope() {
	const service = usePlatformAdminService();
	return useQuery({
		queryKey: moduleEnvelopeKey,
		queryFn: () => service.getModuleEnvelope({}),
	});
}
