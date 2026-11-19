"use client";

import { useQuery } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	getSortedRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useMemo } from "react";
import { OrgSelector } from "@/components/org-selector";
import { activityLabel } from "@/features/permissions/model/activity-label";
import type { EffectivePermission } from "@/features/permissions/model/effective";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import {
	GrantSourceBadge,
	sourceKey,
} from "@/features/permissions/ui/grant-source";
import { ManageMemberRolesDialog } from "@/features/roles/ui/manage-member-roles-dialog";
import { RevokeRoleButton } from "@/features/roles/ui/revoke-role-button";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome } from "@/shared/lib/read-outcome";
import {
	Button,
	EmptyState,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Spinner,
	Stack,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import { userQueries } from "../service/queries";

export function UserDetailPage({ userId }: { userId: string }) {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	return (
		<UserDetail
			key={orgId}
			userId={userId}
			orgId={orgId}
			canGrant={hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)}
		/>
	);
}

function UserDetail({
	userId,
	orgId,
	canGrant,
}: {
	userId: string;
	orgId: string;
	canGrant: boolean;
}) {
	const {
		data: user,
		isLoading: userLoading,
		isError: userFailed,
		error: userError,
		refetch: refetchUser,
	} = useQuery(userQueries.detail(userId));
	const {
		permissions,
		teams,
		roles,
		directAssignments,
		isLoading,
		isError,
		error,
		refetch: refetchAccess,
	} = useEffectivePermissions(orgId, userId);

	if (userFailed || isError) {
		return (
			<PageBody>
				<PageHeader title="User access" />
				<ErrorState
					title="Couldn't load this person's access"
					detail={failureText("view this person's access", userError || error)}
				/>
				{readOutcome(true, userError || error) !== "forbidden" && (
					<Button
						variant="outline"
						onClick={() => {
							void refetchUser();
							void refetchAccess();
						}}
					>
						Try again
					</Button>
				)}
			</PageBody>
		);
	}
	if (userLoading || isLoading)
		return (
			<PageBody>
				<PageHeader title="User access" />
				<Spinner label="Loading this person's access" />
			</PageBody>
		);
	if (!orgId)
		return (
			<PageBody>
				<PageHeader
					title="User access"
					description="Select an organization to review this person's assigned roles."
					actions={<OrgSelector />}
				/>
			</PageBody>
		);

	return (
		<PageBody>
			<PageHeader
				title={user?.profile.name || user?.primaryEmail || "User access"}
				description="Review this person’s assigned roles and team memberships."
				actions={
					<Stack direction="row" gap={2} align="center">
						<OrgSelector />
						{canGrant && orgId && (
							<ManageMemberRolesDialog
								orgId={orgId}
								userId={userId}
								userLabel={user?.primaryEmail || "this person"}
							/>
						)}
						<Button
							variant="outline"
							size="sm"
							nativeButton={false}
							render={<Link href="/admin/users" />}
						>
							<ArrowLeft className="mr-1 h-4 w-4" />
							All users
						</Button>
					</Stack>
				}
			>
				{user?.profile.name && (
					<p className="text-sm text-muted-foreground">{user.primaryEmail}</p>
				)}
			</PageHeader>

			<Section
				title="Direct role assignments"
				description="Roles assigned directly to this person."
			>
				<Panel>
					<DirectRolesTable
						assignments={directAssignments}
						roles={roles}
						canGrant={canGrant}
						userId={userId}
						orgId={orgId}
						userLabel={
							user?.profile.name || user?.primaryEmail || "this person"
						}
					/>
				</Panel>
			</Section>

			<Section
				title="Teams"
				description="A team's roles reach every member, so these are part of what this person can do."
			>
				<Panel>
					<MemberTeamsTable teams={teams} />
				</Panel>
			</Section>

			<Section
				title="Access from assigned roles"
				description="Access provided by the roles and teams listed here. Organization membership and platform-wide access are not included."
			>
				<Panel>
					{permissions.length === 0 ? (
						<EmptyState
							heading="No additional assigned access"
							description="Assign a role or add this person to a team to provide additional access. Organization membership and platform access are managed separately."
						/>
					) : (
						<AssignedAccessTable permissions={permissions} />
					)}
				</Panel>
			</Section>
		</PageBody>
	);
}

const accessColumn = createColumnHelper<EffectivePermission>();
function AssignedAccessTable({
	permissions,
}: {
	permissions: EffectivePermission[];
}) {
	const columns = useMemo(
		() => [
			accessColumn.accessor("permission", {
				header: "Activity",
				cell: ({ row }) =>
					activityLabel(row.original) || "Additional assigned access",
			}),
			accessColumn.display({
				id: "sources",
				header: "Granted by",
				cell: ({ row }) => (
					<Stack direction="row" gap={2} className="flex-wrap">
						{row.original.sources.map((source) => (
							<GrantSourceBadge key={sourceKey(source)} source={source} />
						))}
					</Stack>
				),
			}),
		],
		[],
	);
	const table = useReactTable({
		data: permissions,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});
	return <DataTable table={table} />;
}

type Access = ReturnType<typeof useEffectivePermissions>;
const roleColumn = createColumnHelper<Access["directAssignments"][number]>();
function DirectRolesTable({
	assignments,
	roles,
	canGrant,
	userId,
	orgId,
	userLabel,
}: {
	assignments: Access["directAssignments"];
	roles: Access["roles"];
	canGrant: boolean;
	userId: string;
	orgId: string;
	userLabel: string;
}) {
	const columns = useMemo(() => {
		const names = new Map(roles.map((role) => [role.id, role.name]));
		return [
			roleColumn.accessor(
				(assignment) => names.get(assignment.roleId) || "Role unavailable",
				{
					id: "role",
					header: "Assigned role",
					cell: ({ row }) => (
						<Link
							className="font-medium underline-offset-4 hover:underline focus-visible:outline-ring"
							href={`/admin/roles/${row.original.roleId}`}
						>
							{names.get(row.original.roleId) || "Role unavailable"}
							{row.original.scope ? ` in ${row.original.scope}` : ""}
						</Link>
					),
				},
			),
			...(canGrant
				? [
						roleColumn.display({
							id: "actions",
							header: "Actions",
							cell: ({ row }) => (
								<RevokeRoleButton
									subjectId={userId}
									subjectLabel={userLabel}
									roleId={row.original.roleId}
									roleName={names.get(row.original.roleId) || "this role"}
									orgId={orgId}
									scope={row.original.scope}
								/>
							),
						}),
					]
				: []),
		];
	}, [roles, canGrant, userId, orgId, userLabel]);
	const table = useReactTable({
		data: assignments,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});
	return (
		<DataTable
			table={table}
			emptyMessage={
				canGrant
					? "No roles are assigned directly. Use Manage roles to assign the first role."
					: "No roles are assigned directly. An organization administrator can assign a role."
			}
		/>
	);
}
const teamColumn = createColumnHelper<Access["teams"][number]>();
const teamColumns = [
	teamColumn.accessor("name", {
		header: "Team",
		cell: ({ row }) => (
			<Link
				className="font-medium underline-offset-4 hover:underline focus-visible:outline-ring"
				href={`/admin/teams/${row.original.id}`}
			>
				{row.original.name}
			</Link>
		),
	}),
];
function MemberTeamsTable({ teams }: { teams: Access["teams"] }) {
	const table = useReactTable({
		data: teams,
		columns: teamColumns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		getSortedRowModel: getSortedRowModel(),
	});
	return (
		<DataTable
			table={table}
			emptyMessage="Not a member of any team in this organization. A team administrator can add this person from the team's page."
		/>
	);
}
