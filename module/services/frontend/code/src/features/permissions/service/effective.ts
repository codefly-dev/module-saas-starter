"use client";

import { useQueries, useQuery } from "@tanstack/react-query";
import { useRoles } from "@/features/roles/service/queries";
import { teamQueries } from "@/features/teams/service/queries";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import { usePermissionService } from "@/lib/hooks/use-api-client";
import {
	type Assignment,
	type EffectivePermission,
	resolveEffectivePermissions,
} from "../model/effective";

// Everything this reads is bounded by the subject: the assignments granted to
// them, the teams they belong to, and the assignments granted to those teams.
// Asking for the organization's whole assignment list instead would make one
// person's page cost grow with the tenant, on a surface an administrator opens
// per person.
export function useEffectivePermissions(orgId: string, subjectId: string) {
	const service = usePermissionService();
	const roles = useRoles(orgId);

	const direct = useQuery({
		queryKey: ["role-assignments", orgId, subjectId, SubjectKind.PRINCIPAL],
		enabled: !!orgId && !!subjectId,
		queryFn: () =>
			service.listRoleAssignments({
				orgId,
				subjectId,
				subjectKind: SubjectKind.PRINCIPAL,
			}),
	});

	const teams = useQuery({
		...teamQueries.list(orgId, subjectId),
		enabled: !!orgId && !!subjectId,
	});
	const memberTeams = teams.data?.teams ?? [];

	const teamGrants = useQueries({
		queries: memberTeams.map((team) => ({
			queryKey: ["role-assignments", orgId, team.id, SubjectKind.TEAM],
			queryFn: () =>
				service.listRoleAssignments({
					orgId,
					subjectId: team.id,
					subjectKind: SubjectKind.TEAM,
				}),
		})),
	});

	const assignments: Assignment[] = [
		...(direct.data?.assignments ?? []),
		...teamGrants.flatMap((query) => query.data?.assignments ?? []),
	].map((assignment) => ({
		subjectId: assignment.subjectId,
		roleId: assignment.roleId,
		scope: assignment.scope,
	}));

	const permissions: EffectivePermission[] = resolveEffectivePermissions({
		subjectId,
		roles: roles.data ?? [],
		assignments,
		teams: memberTeams.map((team) => ({ id: team.id, name: team.name })),
	});

	return {
		permissions,
		teams: memberTeams,
		roles: roles.data ?? [],
		directAssignments: direct.data?.assignments ?? [],
		isLoading:
			roles.isLoading ||
			direct.isLoading ||
			teams.isLoading ||
			teamGrants.some((query) => query.isLoading),
	};
}
