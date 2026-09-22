"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, X } from "lucide-react";
import Link from "next/link";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import { useEffectivePermissions } from "@/features/permissions/service/effective";
import {
	GrantSourceBadge,
	sourceKey,
} from "@/features/permissions/ui/grant-source";
import { useRevokeRole } from "@/features/roles/service/mutations";
import { ManageMemberRolesDialog } from "@/features/roles/ui/manage-member-roles-dialog";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Page as PageBody,
	PageHeader,
	Panel,
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
	const { data: user } = useQuery(userQueries.detail(userId));
	const { permissions, teams, roles, directAssignments, isLoading } =
		useEffectivePermissions(orgId, userId);

	const roleNameById = new Map(
		roles.map((role) => [role.id, role.name] as const),
	);
	const direct = directAssignments;
	const revokeRole = useRevokeRole();

	return (
		<PageBody>
			<PageHeader
				title={user?.primaryEmail || truncateUUID(userId)}
				description="Roles, teams, and the permissions they add up to in the selected organization."
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
				<span className="select-all font-mono text-xs text-muted-foreground">
					{userId}
				</span>
			</PageHeader>

			<Section
				title="Direct role assignments"
				description="Roles granted to this principal itself."
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
										<button
											type="button"
											aria-label="Revoke"
											disabled={revokeRole.isPending}
											className="ml-1 hover:text-destructive disabled:opacity-50"
											onClick={() =>
												revokeRole.mutate(
													{
														subjectId: userId,
														roleId: assignment.roleId,
														orgId,
														scope: assignment.scope,
													},
													{
														onSuccess: () => toast.success("Role revoked"),
														onError: (error) =>
															toast.error(
																`Failed to revoke: ${(error as Error).message}`,
															),
													},
												)
											}
										>
											<X className="h-3 w-3" />
										</button>
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
				title="Effective permissions"
				description="Resolved from this organization's role assignments. A permission with two sources survives revoking either one."
			>
				<Panel>
					{isLoading ? (
						<span className="text-sm text-muted-foreground">Loading…</span>
					) : permissions.length === 0 ? (
						<span className="text-sm text-muted-foreground">
							No permissions in this organization.
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
