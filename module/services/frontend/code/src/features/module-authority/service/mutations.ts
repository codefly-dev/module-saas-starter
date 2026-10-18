import { useMutation, useQueryClient } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";
import { moduleEnvelopeKey } from "./queries";

/**
 * Write the next revision. `expectedRevision` is the revision the draft was
 * started from — never the latest one read — so a revision another
 * administrator wrote meanwhile is refused by the host rather than overwritten.
 */
export function usePutModuleEnvelope() {
	const service = usePlatformAdminService();
	const client = useQueryClient();
	return useMutation({
		mutationFn: (input: { expectedRevision: bigint; document: string }) =>
			service.putModuleEnvelope(input),
		onSuccess: () => client.invalidateQueries({ queryKey: moduleEnvelopeKey }),
	});
}
