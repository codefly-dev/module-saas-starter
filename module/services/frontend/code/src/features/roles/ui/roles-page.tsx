"use client";

import { OrgSelector } from "@/components/org-selector";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission, isSuperAdmin } from "@/lib/permissions";
import { Button, Page, PageHeader, Stack } from "@/shared/ui";
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
	const { data: roles = [], isLoading, isError, refetch } = useRoles(orgId);

	return (
		<Page>
			<PageHeader
				title="Roles"
				description={
					orgId
						? "Manage roles and permissions for the selected organization."
						: "Global roles. Select an organization to manage its roles."
				}
				actions={
					<Stack direction="row" gap={3} align="center">
						<OrgSelector />
						{canCreate && <RoleForm orgId={orgId} />}
					</Stack>
				}
			/>

			{!orgId && (
				<p>Select an organization to create or manage its custom roles.</p>
			)}
			{isError ? (
				<div role="alert">
					Couldn&apos;t load roles.{" "}
					<Button variant="outline" onClick={() => refetch()}>
						Try again
					</Button>
				</div>
			) : (
				<RolesTable roles={roles as Role[]} isLoading={isLoading} />
			)}
		</Page>
	);
}
