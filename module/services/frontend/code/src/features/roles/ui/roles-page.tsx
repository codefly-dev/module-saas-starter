"use client";

import { OrgSelector } from "@/components/org-selector";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission, isSuperAdmin } from "@/lib/permissions";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Stack,
} from "@/shared/ui";
import type { Role } from "../model/types";
import { useRoles } from "../service/queries";
import { RoleForm } from "./role-form";
import { RolesTable } from "./roles-table";

export function RolesPage() {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	const canCreate = orgId
		? hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)
		: isSuperAdmin(platformRole);

	// A tenant switch closes any open draft instead of silently retargeting it.
	return (
		<RolesPageForOrganization key={orgId} orgId={orgId} canCreate={canCreate} />
	);
}

function RolesPageForOrganization({
	orgId,
	canCreate,
}: {
	orgId: string;
	canCreate: boolean;
}) {
	const {
		data: roles = [],
		isLoading,
		isError,
		error,
		refetch,
	} = useRoles(orgId);
	// A refusal is not cured by asking again, so only a read that did not come
	// back offers the retry.
	const refused = readOutcome(isError, error) === "forbidden";

	return (
		<PageBody>
			<PageHeader
				title="Roles"
				description={
					orgId
						? "Manage roles and permissions for the selected organization."
						: "Roles shared by every organization. Select an organization to create or manage its own roles."
				}
				actions={
					<>
						<OrgSelector />
						{canCreate && <RoleForm orgId={orgId} />}
					</>
				}
			/>

			{isError ? (
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load the roles"
							detail={
								refused
									? "You don't have permission to see these roles. Ask an organization administrator for access."
									: "Whether any are defined is unknown. Try again; if it keeps failing, tell your administrator."
							}
						/>
						{!refused && (
							<Button variant="outline" size="sm" onClick={() => refetch()}>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			) : (
				<RolesTable
					roles={roles as Role[]}
					isLoading={isLoading}
					canDelete={canCreate}
				/>
			)}
		</PageBody>
	);
}
