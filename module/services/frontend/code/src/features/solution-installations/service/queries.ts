import { createClient } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { OfferService } from "@/gen/host/installation/v1/installation_pb";
import {
	type AvailableSolution,
	InstallationStatus,
	type OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { apiTransport } from "@/lib/connect/transport";
import { useInstallationService } from "@/lib/hooks/use-api-client";

const offers = createClient(OfferService, apiTransport);

// The largest page each listing serves. Both listings are bounded per call, so
// the page reads every page rather than showing the first and implying it is
// the whole set.
const PAGE_SIZE = 500;

export const solutionInstallationKeys = {
	available: (orgId: string) => ["available-solutions", orgId] as const,
	installed: (orgId: string) => ["solution-installations", orgId] as const,
	offered: (orgId: string) => ["solution-offers", orgId] as const,
};

/**
 * The targets this organization's administrator may choose, by route alias:
 * the solutions and the module pages declaring roles, from the authority's
 * one ListOffers read. A module not among them declares no roles: it is a
 * supporting module, installed by a closure. One read answers every row, so
 * nothing asks the authority per module.
 */
export function useOfferedTargets(orgId: string) {
	return useQuery({
		queryKey: solutionInstallationKeys.offered(orgId),
		enabled: !!orgId,
		retry: false,
		refetchOnWindowFocus: false,
		queryFn: async () =>
			new Set(
				(await offers.listOffers({ organizationId: orgId })).offers.map(
					(offer) => offer.target,
				),
			),
	});
}

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
