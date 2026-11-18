"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { OrgSelector } from "@/components/org-selector";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import {
	GrantSourceBadge,
	sourceKey,
} from "@/features/permissions/ui/grant-source";
import { RevokeRoleButton } from "@/features/roles/ui/revoke-role-button";
import { ManageMemberRolesDialog } from "@/features/roles/ui/manage-member-roles-dialog";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { failureText } from "@/shared/lib/failure-text";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Page as PageBody,
	PageHeader,
	Panel,
	ErrorState,
	Spinner,
	Section,
	Stack,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
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
	} = useQuery(userQueries.detail(userId));
	const {
		permissions,
		teams,
		roles,
		directAssignments,
		isLoading,
		isError,
		error,
	} = useEffectivePermissions(orgId, userId);

	const roleNameById = new Map(
		roles.map((role) => [role.id, role.name] as const),
	);
	const direct = directAssignments;

	if (userFailed || isError) {
		return (
			<PageBody>
				<PageHeader title="User access" />
				<ErrorState
					title="Couldn't load this person's access"
					detail={failureText("view this person's access", userError || error)}
				/>
			</PageBody>
		);
	}
	if (userLoading || isLoading)
		return (
			<PageBody>
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
								userLabel={user?.primaryEmail || truncateUUID(userId)}
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
					{direct.length === 0 ? (
						<span className="text-sm text-muted-foreground">
							No roles are assigned directly.
						</span>
					) : (
						<Stack direction="row" gap={2} className="flex-wrap">
							{direct.map((assignment) => (
								<Badge
									key={assignment.id}
									variant="secondary"
									className="gap-1 font-mono text-xs"
								>
									<Link
										href={`/admin/roles/${assignment.roleId}`}
										className="hover:underline"
									>
										{roleNameById.get(assignment.roleId) ??
											truncateUUID(assignment.roleId)}
										{assignment.scope ? ` in ${assignment.scope}` : ""}
									</Link>
									{canGrant && (
										<RevokeRoleButton
											subjectId={userId}
											subjectLabel={
												user?.profile.name ||
												user?.primaryEmail ||
												"this person"
											}
											roleId={assignment.roleId}
											roleName={
												roleNameById.get(assignment.roleId) || "this role"
											}
											orgId={orgId}
											scope={assignment.scope}
										/>
									)}
								</Badge>
							))}
						</Stack>
					)}
				</Panel>
			</Section>

			<Section
				title="Teams"
				description="A team's roles reach every member, so these are part of what this person can do."
			>
				<Panel>
					{teams.length === 0 ? (
						<span className="text-sm text-muted-foreground">
							Not a member of any team in this organization.
						</span>
					) : (
						<Stack direction="row" gap={2} className="flex-wrap">
							{teams.map((team) => (
								<Badge key={team.id} variant="outline">
									<Link
										href={`/admin/teams/${team.id}`}
										className="hover:underline"
									>
										{team.name}
									</Link>
								</Badge>
							))}
						</Stack>
					)}
				</Panel>
			</Section>

			<Section
				title="Access from assigned roles"
				description="Access provided by the roles and teams listed here. Organization membership and platform-wide access are not included."
			>
				<Panel>
					{isLoading ? (
						<span className="text-sm text-muted-foreground">Loading…</span>
					) : permissions.length === 0 ? (
						<span className="text-sm text-muted-foreground">
							No additional access from assigned roles in this organization.
						</span>
					) : (
						<Table>
							<TableHeader>
								<TableRow>
									<TableHead>Permission</TableHead>
									<TableHead>Granted by</TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{permissions.map((entry) => (
									<TableRow key={entry.permission}>
										<TableCell className="font-mono text-xs">
											{entry.permission}
										</TableCell>
										<TableCell>
											<Stack direction="row" gap={2} className="flex-wrap">
												{entry.sources.map((source) => (
													<GrantSourceBadge
														key={sourceKey(source)}
														source={source}
													/>
												))}
											</Stack>
										</TableCell>
									</TableRow>
								))}
							</TableBody>
						</Table>
					)}
				</Panel>
			</Section>
		</PageBody>
	);
}
