import { useQuery } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

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
	return useQuery({
		queryKey: ["module-tenant-identity", prefix, binding, organizationId],
		enabled: !!prefix && !!binding && !!organizationId,
		queryFn: () =>
			service.resolveModuleTenantIdentity({ prefix, binding, organizationId }),
	});
}
