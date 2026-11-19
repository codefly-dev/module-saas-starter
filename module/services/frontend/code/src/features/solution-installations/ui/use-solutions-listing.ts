"use client";

import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import { useOrgMembers } from "@/lib/hooks/use-organizations";
import type { AccessView } from "../model/access";
import { isModulePage } from "../model/installations";
import {
	isChosen,
	isListed,
	isListedInstalled,
	type ListingScope,
	modulesWithheld,
	pageAccess,
	roleGrantedInstallations,
} from "../model/listing";
import {
	useAuthorityInstallations,
	useAvailableSolutions,
	useInstallations,
	useOfferedTargets,
	useRoleGrants,
} from "../service/queries";

type Target = AvailableSolution | undefined;

/**
 * Everything the Solutions page reads, and what each read decides: which
 * targets each card lists, and who holds which role on what is installed.
 *
 * Which targets an administrator installs is the authority's one answer
 * (ListOffers). Who holds which role on each installation the authority
 * grants roles on is read on the Solutions view only, named as the
 * organization names its teams and people.
 */
export function useSolutionsListing(orgId: string, showModules: boolean) {
	const available = useAvailableSolutions(orgId);
	const installed = useInstallations(orgId);
	const offered = useOfferedTargets(orgId);
	const held = useAuthorityInstallations(showModules ? "" : orgId);
	const teams = useQuery({
		...teamQueries.list(orgId),
		enabled: !showModules && Boolean(orgId),
	});
	const members = useOrgMembers(showModules ? null : orgId);
	const roleGranted = useMemo(
		() => (showModules ? [] : roleGrantedInstallations(installed.data ?? [])),
		[showModules, installed.data],
	);
	const roleGrants = useRoleGrants(orgId, roleGranted);

	const scope: ListingScope = useMemo(
		() => ({
			showModules,
			offersRead: offered.isSuccess,
			offered: offered.data,
		}),
		[showModules, offered.isSuccess, offered.data],
	);

	const access: AccessView = {
		grants: roleGrants.grants,
		failed: roleGrants.failed,
		unreached: (id) => held.data?.get(id)?.unreached ?? [],
		roles: (id) => held.data?.get(id)?.roles ?? [],
		name: (grant) =>
			grant.teamId
				? (teams.data?.teams.find((team) => team.id === grant.teamId)?.name ??
					grant.teamId)
				: members.data?.find((member) => member.userId === grant.personId)
						?.userEmail || grant.personId,
	};

	const accessOf = useCallback(
		(page: AvailableSolution) =>
			pageAccess(
				page,
				installed.data ?? [],
				(id) => roleGrants.grants.get(id),
				(id) => held.data?.get(id)?.roles,
			),
		[installed.data, roleGrants.grants, held.data],
	);

	return {
		available,
		installed,
		offered,
		access,
		accessOf,
		/** No module is classified until the offers are read. */
		modulesWithheld: modulesWithheld(scope),
		chosen: useCallback((target: Target) => isChosen(target, scope), [scope]),
		/** Supporting module administration's one catalogue. */
		listedModule: useCallback(
			(target: Target) => isListed(target, scope),
			[scope],
		),
		listedSolution: useCallback(
			(target: Target) => isListed(target, scope) && !isModulePage(target),
			[scope],
		),
		listedModulePage: useCallback(
			(target: Target) => isListed(target, scope) && isModulePage(target),
			[scope],
		),
		listedInstalled: useCallback(
			(target: Target) => isListedInstalled(target, scope),
			[scope],
		),
	};
}
