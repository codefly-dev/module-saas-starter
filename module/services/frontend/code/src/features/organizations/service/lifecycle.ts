"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/lib/auth";
import { fallbackOrganization } from "../model/leave-flow";
import { orgMutations } from "./mutations";
import { orgQueries } from "./queries";

function invalidateOrganization(
	queryClient: ReturnType<typeof useQueryClient>,
	orgId: string,
) {
	queryClient.invalidateQueries({ queryKey: ["organizations"] });
	queryClient.invalidateQueries({ queryKey: ["org-members", orgId] });
	queryClient.invalidateQueries({ queryKey: ["org-roster", orgId] });
	queryClient.invalidateQueries({ queryKey: ["platform-organizations"] });
}

/**
 * Runs an action that ends the caller's membership of `orgId` — leaving it or
 * deleting it. When the browser is signed into that organization, the session
 * moves to another one first: the membership's removal revokes every session
 * bound to it. With nowhere to move, the action still runs and the user is
 * signed out, which is the honest outcome — they belong to no organization.
 */
function useDeparture() {
	const queryClient = useQueryClient();
	const { organizationId, switchOrganization, logout } = useAuth();
	return async (orgId: string, action: () => Promise<unknown>) => {
		let stranded = false;
		if (organizationId === orgId) {
			const { organizations } = await queryClient.fetchQuery(orgQueries.list());
			const next = fallbackOrganization(organizations, orgId);
			if (next) {
				await switchOrganization(next.id);
			} else {
				stranded = true;
			}
		}
		await action();
		invalidateOrganization(queryClient, orgId);
		if (stranded) await logout();
	};
}

export function useLeaveOrganization() {
	const depart = useDeparture();
	return useMutation({
		mutationFn: (orgId: string) =>
			depart(orgId, () => orgMutations.leave(orgId)),
	});
}

export function useDeleteOrganization() {
	const depart = useDeparture();
	return useMutation({
		mutationFn: ({
			orgId,
			confirmSlug,
		}: {
			orgId: string;
			confirmSlug: string;
		}) => depart(orgId, () => orgMutations.delete(orgId, confirmSlug)),
	});
}

export function useUpdateOrganization() {
	const queryClient = useQueryClient();
	return useMutation({
		mutationFn: ({
			orgId,
			name,
			slug,
		}: {
			orgId: string;
			name: string;
			slug: string;
		}) => orgMutations.update(orgId, name, slug),
		onSuccess: (_org, { orgId }) => {
			invalidateOrganization(queryClient, orgId);
			queryClient.invalidateQueries({ queryKey: ["organizations", orgId] });
		},
	});
}
