import { useQuery, useQueryClient } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";
import { mayKeepRetainedRows, readOutcome } from "@/shared/lib/read-outcome";

/**
 * The read behind the per-tenant module identity playground.
 *
 * It resolves what one module's capability WOULD assert for one organization,
 * and issues nothing: the mint needs the module pod's projected ServiceAccount
 * token held against the build its authority document approves, which a browser
 * has none of — and a path that answered an operator with a module's capability
 * would be a second spelling of that module's identity.
 *
 * `enabled` is every field, because the answer is about the triple. A read with
 * two of the three would be asking a different question and the page would show
 * its answer as if it were this one's.
 */
export function useModuleTenantIdentity(
	prefix: string,
	binding: string,
	organizationId: string,
) {
	const service = usePlatformAdminService();
	const client = useQueryClient();
	return useQuery({
		queryKey: ["module-tenant-identity", prefix, binding, organizationId],
		enabled: !!prefix && !!binding && !!organizationId,
		queryFn: async () => {
			try {
				return await service.resolveModuleTenantIdentity({
					prefix,
					binding,
					organizationId,
				});
			} catch (error) {
				if (!mayKeepRetainedRows(readOutcome(true, error))) {
					for (const query of client
						.getQueryCache()
						.findAll({ queryKey: ["module-tenant-identity"] })) {
						query.setState({ data: undefined, dataUpdatedAt: 0 });
					}
				}
				throw error;
			}
		},
	});
}
