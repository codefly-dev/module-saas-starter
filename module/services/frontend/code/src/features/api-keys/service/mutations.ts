import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAPIKeyService } from "@/lib/hooks/use-api-client";

export function useCreateAPIKey() {
	const svc = useAPIKeyService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			organizationId,
			name,
			scopes,
			environment,
		}: {
			organizationId: string;
			name: string;
			scopes?: { resource: string; action: string }[];
			environment?: number;
		}) =>
			svc.createAPIKey({
				organizationId,
				name,
				scopes: scopes ?? [],
				environment: environment ?? 1,
			}),
		// Reveal the one-time secret immediately, even if refreshing the list is slow.
		onSuccess: () => {
			void qc.invalidateQueries({ queryKey: ["api-keys"] });
		},
	});
}

export function useRevokeAPIKey() {
	const svc = useAPIKeyService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (input: { id: string; organizationId: string }) =>
			svc.revokeAPIKey(input),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["api-keys"] }),
	});
}
