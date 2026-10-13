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
			// The answer this fetch is reading, so a superseded refusal cannot
			// delete a newer one (issue #1121; the same guard the declaration
			// read carries, for the same race).
			const key = [
				"module-tenant-identity",
				prefix,
				binding,
				organizationId,
			] as const;
			const readingAnswerFrom =
				client.getQueryCache().find({ queryKey: key, exact: true })?.state
					.dataUpdatedAt ?? 0;
			try {
				return await service.resolveModuleTenantIdentity({
					prefix,
					binding,
					organizationId,
				});
			} catch (error) {
				// The refused triple, and only it. `findAll({ queryKey })` is a
				// PREFIX match, so this cleared every cached triple — including
				// answers about other bindings and other organizations that
				// nothing had refused. This page asks one triple at a time, so
				// the harm was a needless refetch rather than a false claim, but
				// the rule is the same one the declaration query now follows
				// (issue #1121): a refusal is evidence about the key it
				// answered, and about no other.
				if (!mayKeepRetainedRows(readOutcome(true, error))) {
					const entry = client
						.getQueryCache()
						.find({ queryKey: key, exact: true });
					if (entry && entry.state.dataUpdatedAt <= readingAnswerFrom) {
						entry.setState({ data: undefined, dataUpdatedAt: 0 });
					}
				}
				throw error;
			}
		},
	});
}
