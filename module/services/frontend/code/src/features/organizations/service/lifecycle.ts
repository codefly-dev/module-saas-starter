"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/lib/auth";
import { signOutAndLeave } from "@/lib/sign-out-journey";
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

/** Submit the departure under the current tenant. Once it commits, its
 * membership can no longer authorize requests. Move to a remaining workspace
 * if the session still permits the exchange, otherwise finish signing out. */
function useDeparture() {
	const queryClient = useQueryClient();
	const { organizationId, switchOrganization, logout } = useAuth();
	return async (orgId: string, action: () => Promise<unknown>) => {
		const current = organizationId === orgId;
		const next = current
			? fallbackOrganization((await queryClient.fetchQuery(orgQueries.list())).organizations, orgId)
			: undefined;
		await action();
		invalidateOrganization(queryClient, orgId);
		if (!current) return;
		if (next) {
			try {
				await switchOrganization(next.id);
				return;
			} catch {
				// The removed membership's session may already have been revoked.
			}
		}
		await signOutAndLeave(logout);
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
