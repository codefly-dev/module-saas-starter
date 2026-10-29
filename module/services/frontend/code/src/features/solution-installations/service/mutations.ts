import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReviewedSolutionInstall } from "@/gen/saas/accounts/v1/installations_pb";
import { useInstallationService } from "@/lib/hooks/use-api-client";
import { parseList } from "../model/installations";
import type { InstallSolutionValues } from "../model/schemas";
import { solutionInstallationKeys } from "./queries";

/**
 * Installs one available target. The target is the immutable identity the
 * catalogue row named — never a route alias — and the owner of record is left
 * empty so the host records the calling administrator, which it requires to be
 * a current one.
 */
export function useInstallModule(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({
			targetId,
			generation,
			expectedRevision,
			operationId,
			values,
		}: {
			targetId: string;
			generation: bigint;
			expectedRevision: bigint;
			operationId: string;
			values: InstallSolutionValues;
		}) =>
			service.installSolution({
				orgId,
				targetId,
				approval: { targetId, generation, expectedRevision, operationId },
				agentIdentifier: values.agentIdentifier.trim(),
				displayName: values.displayName.trim(),
				rootScopeLabel: values.rootScopeLabel.trim(),
				roleId: values.roleId,
				allowedAudiences: parseList(values.allowedAudiences),
				allowedScopes: parseList(values.allowedScopes),
			}),
		onSettled: () => invalidate(queryClient, orgId),
	});
}

/**
 * Uninstalls one installation. The host may refuse, and the caller shows that
 * refusal; nothing here turns it into a success.
 */
export function useUninstallSolution(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (installationId: string) =>
			service.uninstallSolution({ orgId, installationId }),
		onSettled: () => invalidate(queryClient, orgId),
	});
}

export function useRetryInstallation(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (installationId: string) =>
			service.retryInstallation({ orgId, installationId }),
		onSettled: () => invalidate(queryClient, orgId),
	});
}

export interface TeamExposure {
	installationId: string;
	teamId: string;
	roleId: string;
}

/**
 * Exposes an installation to a team. The page names the installation and the
 * team; the host resolves where the grant is written, and refuses a role that
 * would expose nothing.
 */
export function useGrantInstallationToTeam(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({ installationId, teamId, roleId }: TeamExposure) =>
			service.grantInstallationToTeam({
				orgId,
				installationId,
				teamId,
				roleId,
			}),
		onSettled: () => invalidate(queryClient, orgId),
	});
}

/**
 * Removes one team's grant at an installation. The host may refuse it, and the
 * caller shows that refusal as the host words it.
 */
export function useRevokeInstallationFromTeam(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({ installationId, teamId, roleId }: TeamExposure) =>
			service.revokeInstallationFromTeam({
				orgId,
				installationId,
				teamId,
				roleId,
			}),
		onSettled: () => invalidate(queryClient, orgId),
	});
}

function invalidate(
	queryClient: ReturnType<typeof useQueryClient>,
	orgId: string,
) {
	return Promise.all([
		queryClient.invalidateQueries({
			queryKey: solutionInstallationKeys.available(orgId),
		}),
		queryClient.invalidateQueries({
			queryKey: solutionInstallationKeys.installed(orgId),
		}),
	]);
}

/** Submit the original server review, including each supporting consent. */
export function useInstallReviewedSolution(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: (review: ReviewedSolutionInstall) =>
			service.installReviewedSolution({ orgId, review }),
		onSettled: () => invalidate(queryClient, orgId),
	});
}
