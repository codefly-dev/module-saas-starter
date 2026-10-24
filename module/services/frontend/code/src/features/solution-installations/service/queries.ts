import { useQuery } from "@tanstack/react-query";
import { InstallationStatus } from "@/gen/saas/accounts/v1/installation_listing_pb";
import type {
	AvailableSolution,
	OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { useInstallationService } from "@/lib/hooks/use-api-client";

// The largest page each listing serves. Both listings are bounded per call, so
// the page reads every page rather than showing the first and implying it is
// the whole set.
const PAGE_SIZE = 500;

export const solutionInstallationKeys = {
	available: (orgId: string) => ["available-solutions", orgId] as const,
	installed: (orgId: string) => ["solution-installations", orgId] as const,
};

/**
 * What this organization may install, from ListAvailableSolutions. Tenancy and
 * acceptance are the host's: the page offers exactly what this read returns.
 */
export function useAvailableSolutions(orgId: string) {
	const service = useInstallationService();
	return useQuery({
		queryKey: solutionInstallationKeys.available(orgId),
		enabled: !!orgId,
		queryFn: async () => {
			const solutions: AvailableSolution[] = [];
			let pageToken = "";
			do {
				const page = await service.listAvailableSolutions({
					orgId,
					pageSize: PAGE_SIZE,
					pageToken,
				});
				solutions.push(...page.solutions);
				pageToken = page.nextPageToken;
			} while (pageToken);
			return solutions;
		},
	});
}

/**
 * This organization's current installations, from ListOrganizationInstallations:
 * each with its live health, the catalogue row of the target it installed, and
 * the teams it is exposed to. The organization is bound to the caller's own
 * administration by the host, so this reads nothing but its own.
 */
export function useInstallations(orgId: string) {
	const service = useInstallationService();
	return useQuery({
		queryKey: solutionInstallationKeys.installed(orgId),
		enabled: !!orgId,
		refetchInterval: (query) =>
			query.state.data?.some(
				(row) => row.installation?.status === InstallationStatus.PENDING,
			)
				? 2000
				: false,
		queryFn: async () => {
			const installations: OrganizationInstallation[] = [];
			let pageToken = "";
			do {
				const page = await service.listOrganizationInstallations({
					orgId,
					pageSize: PAGE_SIZE,
					pageToken,
				});
				installations.push(...page.installations);
				pageToken = page.nextPageToken;
			} while (pageToken);
			return installations;
		},
	});
}
