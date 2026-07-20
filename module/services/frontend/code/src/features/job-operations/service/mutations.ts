import { useMutation, useQueryClient } from "@tanstack/react-query";
import { usePlatformAdminService } from "@/lib/hooks/use-api-client";

export function useReplayJob() {
	const service = usePlatformAdminService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (sourceJobId: string) => {
			const idempotencyKey = crypto.randomUUID();
			return service.replayJob(
				{ sourceJobId, idempotencyKey },
				{ headers: { "Idempotency-Key": idempotencyKey } },
			);
		},
		onSuccess: () =>
			Promise.all([
				queryClient.invalidateQueries({ queryKey: ["job-operations"] }),
				queryClient.invalidateQueries({ queryKey: ["jobs"] }),
			]),
	});
}
