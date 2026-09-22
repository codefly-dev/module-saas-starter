"use client";

import { OrgSelector } from "@/components/org-selector";
import { RoleGate } from "@/components/auth/role-gate";
import { useAuth } from "@/lib/auth";
import { Button } from "@/shared/ui";
import type { Role } from "../model/types";
import { useRoles } from "../service/queries";
import { RoleForm } from "./role-form";
import { RolesTable } from "./roles-table";

export function RolesPage() {
	const { organizationId } = useAuth();
	const {
		data: roles = [],
		isLoading,
		isError,
		refetch,
	} = useRoles(organizationId);

	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between">
				<div>
					<h1 className="text-2xl font-bold tracking-tight">Roles</h1>
					<p className="text-muted-foreground">
						Manage built-in roles and custom roles for the selected
						organization.
					</p>
				</div>
				<div className="flex items-center gap-3">
					<RoleGate requirePermission="roles:write">
						<RoleForm key={organizationId} orgId={organizationId} />
					</RoleGate>
					<OrgSelector />
				</div>
			</div>

			{!organizationId && (
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
		</div>
	);
}
