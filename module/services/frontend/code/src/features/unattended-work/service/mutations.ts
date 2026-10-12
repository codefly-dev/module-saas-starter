import { notifySourceReconcileAdmitted } from "@codefly-dev/saas-ui";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useDatasourceService } from "@/lib/hooks/use-api-client";
import { unattendedWorkKeys } from "./queries";

/**
 * The three acts the playground performs, each the host's own operation.
 *
 * They are separate mutations rather than one "configure" call because they
 * are three different decisions: who an occurrence borrows authority from,
 * taking that authority away, and running one pass by hand.
 */

/** Mint the delegation reference a release's triggers run under. */
export function useDelegateSourceTriggers(orgId: string) {
	const service = useDatasourceService();
	const client = useQueryClient();
	return useMutation({
		mutationFn: (sourceId: string) =>
			service.delegateSourceTriggers({ orgId, sourceId }),
		onSuccess: (_result, sourceId) =>
			client.invalidateQueries({
				queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
			}),
	});
}

/**
 * Revoke one reference. The next occurrence is refused — the host re-checks the
 * delegation on every mint, so nothing has to be stopped for this to take
 * effect, and work already in flight is not recalled.
 */
export function useRevokeTriggerDelegation(orgId: string) {
	const service = useDatasourceService();
	const client = useQueryClient();
	return useMutation({
		mutationFn: (input: { sourceId: string; delegationId: string }) =>
			service.revokeSourceDelegation({ orgId, id: input.delegationId }),
		onSuccess: (_result, input) =>
			client.invalidateQueries({
				queryKey: unattendedWorkKeys.declaration(orgId, input.sourceId),
			}),
	});
}

/**
 * Run one reconcile pass by hand: the same host operation a declared
 * schedule's occurrence admits, with an effect identity this press chose.
 *
 * The identity is generated here and not by the server, because that is what
 * makes the pass idempotent: two presses that reach the host under one
 * identity are one pass, and a fresh identity per press is one new pass each.
 * This page mints one PER PRESS deliberately — unlike the kit's "Sync now",
 * which holds one so a slip costs nothing: here a press is the walk being
 * driven, and every press is meant to be a new occurrence.
 *
 * The admitted pass is announced on the kit's registry, so the
 * reconcile-schedules panel lists it beside the declared schedule's
 * occurrences as the same admission.
 */
export function useReconcileSource(orgId: string) {
	const service = useDatasourceService();
	const client = useQueryClient();
	return useMutation({
		mutationFn: async (sourceId: string) => {
			const effectId = crypto.randomUUID();
			const result = await service.reconcileSource({
				orgId,
				sourceId,
				effectId,
			});
			notifySourceReconcileAdmitted({
				sourceId,
				effectId: result.effectId || effectId,
				jobId: result.jobId,
				status: result.status === "committed" ? "committed" : "unknown",
				repeat: false,
				at: new Date().toISOString(),
			});
			return result;
		},
		onSuccess: (_result, sourceId) =>
			client.invalidateQueries({
				queryKey: unattendedWorkKeys.declaration(orgId, sourceId),
			}),
	});
}
