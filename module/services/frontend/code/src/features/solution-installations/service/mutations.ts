import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReviewedSolutionInstall } from "@/gen/saas/accounts/v1/installations_pb";
import { useInstallationService } from "@/lib/hooks/use-api-client";
import type { InstallAccess } from "../model/team-assignment";
import { solutionInstallationKeys } from "./queries";

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

/** Sets up who may use a module page: its roles to teams, in one act. */
export function useSetUpModulePage(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({
			targetId,
			access,
		}: {
			targetId: string;
			access: InstallAccess;
		}) =>
			service.setUpModulePage({
				orgId,
				targetId,
				roleTeams: access.roleTeams,
				installerRoles: access.installerRoles,
			}),
		onSettled: () =>
			Promise.all([
				invalidate(queryClient, orgId),
				queryClient.invalidateQueries({ queryKey: ["teams", orgId] }),
			]),
	});
}

/**
 * Submit the original server review, including each supporting consent, and
 * the access the install assigns; the host records both and applies the
 * access once setup is active.
 */
export function useInstallReviewedSolution(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({
			review,
			access,
		}: {
			review: ReviewedSolutionInstall;
			access: InstallAccess;
		}) =>
			service.installReviewedSolution({
				orgId,
				review,
				roleTeams: access.roleTeams,
				installerRoles: access.installerRoles,
			}),
		onSettled: () =>
			Promise.all([
				invalidate(queryClient, orgId),
				queryClient.invalidateQueries({ queryKey: ["teams", orgId] }),
			]),
	});
}
