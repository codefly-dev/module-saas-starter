import { updateAsks } from "@codefly-dev/saas-ui/solution";
import { createClient } from "@connectrpc/connect";
import { useQueries, useQuery } from "@tanstack/react-query";
import {
	InstallationService as HostInstallationService,
	OfferService,
	type RoleGrant,
} from "@/gen/host/installation/v1/installation_pb";
import {
	type AvailableSolution,
	InstallationStatus,
	type OrganizationInstallation,
} from "@/gen/saas/accounts/v1/installations_pb";
import { apiTransport } from "@/lib/connect/transport";
import { useInstallationService } from "@/lib/hooks/use-api-client";

const offers = createClient(OfferService, apiTransport);
const hostInstallations = createClient(HostInstallationService, apiTransport);

// The largest page each listing serves. Both listings are bounded per call, so
// the page reads every page rather than showing the first and implying it is
// the whole set.
const PAGE_SIZE = 500;

export const solutionInstallationKeys = {
	available: (orgId: string) => ["available-solutions", orgId] as const,
	installed: (orgId: string) => ["solution-installations", orgId] as const,
	offered: (orgId: string) => ["solution-offers", orgId] as const,
	held: (orgId: string) => ["installation-authority", orgId] as const,
	grants: (orgId: string, installationId: string) =>
		["installation-role-grants", orgId, installationId] as const,
};

/**
 * Each installation as the authority holds it, by id: its declared roles and
 * what its roles name through a binding that the composition does not reach
 * now (`unreached`), which the host's own listing does not carry.
 */
export function useAuthorityInstallations(orgId: string) {
	return useQuery({
		queryKey: solutionInstallationKeys.held(orgId),
		enabled: !!orgId,
		retry: false,
		queryFn: async () =>
			new Map(
				(
					await hostInstallations.list({ organizationId: orgId })
				).installations.map((installation) => [installation.id, installation]),
			),
	});
}

/**
 * Who holds which role on each of these installations, as the authority
 * answers ListRoleGrants: one read per installation, keyed by its id. An
 * installation whose read failed is absent, and `failed` names it.
 */
export function useRoleGrants(
	orgId: string,
	installationIds: readonly string[],
) {
	return useQueries({
		queries: installationIds.map((installationId) => ({
			queryKey: solutionInstallationKeys.grants(orgId, installationId),
			enabled: !!orgId,
			retry: false,
			queryFn: async () =>
				(
					await hostInstallations.listRoleGrants({
						organizationId: orgId,
						installationId,
					})
				).grants,
		})),
		combine: (results) => {
			const grants = new Map<string, RoleGrant[]>();
			const failed = new Set<string>();
			results.forEach((result, i) => {
				if (result.data) grants.set(installationIds[i], result.data);
				else if (result.isError) failed.add(installationIds[i]);
			});
			// Still being read: a consumer must not read an absent installation
			// as one nobody holds a role on while its answer is on the way.
			const pending = results.some((result) => result.isPending);
			return { grants, failed, pending };
		},
	});
}

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

/**
 * What an update the authority offers on an installed solution asks beyond
 * what was approved, in words (`updateAsks`): the host's review of the
 * target beside the authority's review of the composition now. `asks` is
 * undefined when the approval did not record its reach.
 */
export function useUpdateAsks(
	orgId: string,
	targetId: string,
	enabled: boolean,
) {
	const service = useInstallationService();
	return useQuery({
		queryKey: ["solution-update-asks", orgId, targetId],
		enabled: enabled && !!orgId && !!targetId,
		retry: false,
		refetchOnWindowFocus: false,
		queryFn: async () => {
			const review = await service.reviewSolutionInstall({ orgId, targetId });
			const offer = await offers.reviewOffer({
				organizationId: orgId,
				target: review.target,
			});
			return {
				asks: review.approvedRoles.length
					? updateAsks(offer.target, offer.roles, review.approvedRoles)
					: undefined,
				approved: review.approvedRoles,
			};
		},
	});
}
