import { useMutation, useQueryClient } from "@tanstack/react-query";
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
export function useInstallSolution(orgId: string) {
	const service = useInstallationService();
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({
			targetId,
			values,
		}: {
			targetId: string;
			values: InstallSolutionValues;
		}) =>
			service.installSolution({
				orgId,
				targetId,
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
 * Uninstalls one installation. The host may refuse — today it refuses every
 * uninstall, because the policy log that must witness a narrowing has no
 * transport — and the caller shows that refusal; nothing here turns it into a
 * success.
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
