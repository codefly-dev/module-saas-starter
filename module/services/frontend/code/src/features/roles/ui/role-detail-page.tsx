"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, Save, X } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import {
	groupByResource,
	permissionLabel,
} from "@/features/permissions/model/effective";
import { permissionQueries } from "@/features/permissions/service/queries";
import { teamQueries } from "@/features/teams/service/queries";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Checkbox,
	Input,
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
import type { Permission, Role } from "../model/types";
import { useRevokeRole, useUpdateRole } from "../service/mutations";
import { useRoleAssignments, useRoles } from "../service/queries";

export function RoleDetailPage({ roleId }: { roleId: string }) {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	// Remounting on a tenant switch drops an in-progress edit rather than
	// carrying it onto a role the new tenant cannot see.
	return (
		<RoleDetail
			key={orgId}
			roleId={roleId}
			orgId={orgId}
			canWrite={hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)}
		/>
	);
}

function RoleDetail({
	roleId,
	orgId,
	canWrite,
}: {
	roleId: string;
	orgId: string;
	canWrite: boolean;
}) {
	const { data: roles = [], isLoading } = useRoles(orgId);
	const role = (roles as Role[]).find((candidate) => candidate.id === roleId);

	if (isLoading) {
		return <PageBody>Loading…</PageBody>;
	}
	if (!role) {
		return (
			<PageBody>
				<PageHeader
					title="Role not found"
					description="No role with this id is visible in the selected organization."
					actions={<OrgSelector />}
				/>
				<BackToRoles />
			</PageBody>
		);
	}

	return (
		<PageBody>
			<PageHeader
				title={role.name}
				description={role.description || "No description."}
				actions={
					<Stack direction="row" gap={2} align="center">
						<OrgSelector />
						<BackToRoles />
					</Stack>
				}
			>
				<Stack direction="row" gap={2} align="center">
					{role.builtIn ? (
						<Badge variant="outline">Built-in</Badge>
					) : (
						<Badge variant="secondary">Custom</Badge>
					)}
					<span className="select-all font-mono text-xs text-muted-foreground">
						{role.id}
					</span>
				</Stack>
			</PageHeader>

			<RolePermissionsEditor
				role={role}
				orgId={orgId}
				canWrite={canWrite && !role.builtIn}
			/>
			<RoleHolders role={role} orgId={orgId} canWrite={canWrite} />
		</PageBody>
	);
}

function BackToRoles() {
	return (
		<Button
			variant="outline"
			size="sm"
			nativeButton={false}
			render={<Link href="/admin/roles" />}
		>
			<ArrowLeft className="mr-1 h-4 w-4" />
			All roles
		</Button>
	);
}

// The editor sends the whole set it wants the role to end up with, which is
// what UpdateRole replaces. Starting from the role's current grants makes the
// unchanged case a no-op rather than a silent clear.
function RolePermissionsEditor({
	role,
	orgId,
	canWrite,
}: {
	role: Role;
	orgId: string;
	canWrite: boolean;
}) {
	const [description, setDescription] = useState(role.description);
	const [granted, setGranted] = useState<Set<string>>(
		() => new Set(role.permissions.map(permissionLabel)),
	);
	const updateRole = useUpdateRole();
	const { data: info } = useQuery(permissionQueries.serviceInfo());

	// The vocabulary the service declares, plus anything this role already
	// holds — a grant the service no longer declares still has to be visible,
	// or saving would quietly drop it.
	const declared = (info?.capabilities?.permissions ?? []).map((permission) =>
		permissionLabel(permission),
	);
	const vocabulary = [...new Set([...declared, ...granted])];
	const groups = groupByResource(vocabulary);

	const toggle = (label: string) => {
		setGranted((current) => {
			const next = new Set(current);
			if (!next.delete(label)) next.add(label);
			return next;
		});
	};

	const save = () => {
		const permissions: Permission[] = [...granted].flatMap((label) => {
			const separator = label.indexOf(":");
			return separator <= 0
				? []
				: [
						{
							resource: label.slice(0, separator),
							action: label.slice(separator + 1),
						},
					];
		});
		updateRole.mutate(
			{ id: role.id, description, permissions, orgId },
			{
				onSuccess: () => toast.success(`Role "${role.name}" updated`),
				onError: (error) =>
					toast.error(`Failed to update role: ${(error as Error).message}`),
			},
		);
	};

	return (
		<Section
			title="Permissions"
			description={
				canWrite
					? "The set saved here becomes the role's whole grant set."
					: "Built-in roles are the platform's standard vocabulary and cannot be edited."
			}
			actions={
				canWrite ? (
					<Button size="sm" onClick={save} disabled={updateRole.isPending}>
						<Save className="mr-1 h-4 w-4" />
						{updateRole.isPending ? "Saving…" : "Save changes"}
					</Button>
				) : undefined
			}
		>
			<Panel>
				<Stack gap={4}>
					<Stack gap={2}>
						<span className="text-sm font-medium">Description</span>
						<Input
							value={description}
							disabled={!canWrite}
							onChange={(event) => setDescription(event.target.value)}
							placeholder="What this role is for"
						/>
					</Stack>
					{groups.map((group) => (
						<Stack key={group.resource} gap={2}>
							<span className="text-sm font-medium">{group.resource}</span>
							<Stack direction="row" gap={4} className="flex-wrap">
								{group.permissions.map((permission) => {
									const label = permissionLabel(permission);
									return (
										<Stack key={label} direction="row" gap={2} align="center">
											<Checkbox
												aria-label={label}
												checked={granted.has(label)}
												disabled={!canWrite}
												onCheckedChange={() => toggle(label)}
											/>
											<span className="font-mono text-xs">{label}</span>
										</Stack>
									);
								})}
							</Stack>
						</Stack>
					))}
				</Stack>
			</Panel>
		</Section>
	);
}

// Who holds this role — principals and teams alike. A role's blast radius is
// both lists, so showing only the direct grants would understate it.
function RoleHolders({
	role,
	orgId,
	canWrite,
}: {
	role: Role;
	orgId: string;
	canWrite: boolean;
}) {
	const { data: assignments = [], isLoading } = useRoleAssignments(
		orgId,
		undefined,
		SubjectKind.UNSPECIFIED,
	);
	const { data: teams } = useQuery(teamQueries.list(orgId));
	const revokeRole = useRevokeRole();

	const teamNameById = new Map(
		(teams?.teams ?? []).map((team) => [team.id, team.name] as const),
	);
	const holders = assignments.filter(
		(assignment) => assignment.roleId === role.id,
	);

	return (
		<Section
			title="Held by"
			description="Every principal and team this role is assigned to in this organization."
		>
			<Panel>
				{isLoading ? (
					<span className="text-sm text-muted-foreground">Loading…</span>
				) : holders.length === 0 ? (
					<span className="text-sm text-muted-foreground">
						Nobody holds this role.
					</span>
				) : (
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>Subject</TableHead>
								<TableHead>Kind</TableHead>
								<TableHead />
							</TableRow>
						</TableHeader>
						<TableBody>
							{holders.map((assignment) => {
								const isTeam = assignment.subjectKind === SubjectKind.TEAM;
								const name = isTeam
									? (teamNameById.get(assignment.subjectId) ??
										truncateUUID(assignment.subjectId))
									: truncateUUID(assignment.subjectId);
								const href = isTeam
									? `/admin/teams/${assignment.subjectId}`
									: `/admin/users/${assignment.subjectId}`;
								return (
									<TableRow key={assignment.id}>
										<TableCell>
											<Link
												href={href}
												className="font-mono text-xs text-primary hover:underline"
											>
												{name}
											</Link>
										</TableCell>
										<TableCell>
											<Badge variant="outline">
												{isTeam ? "Team" : "Principal"}
											</Badge>
											{assignment.scope ? (
												<Badge variant="secondary" className="ml-2">
													{assignment.scope}
												</Badge>
											) : null}
										</TableCell>
										<TableCell>
											{canWrite && (
												<Button
													variant="ghost"
													size="sm"
													disabled={revokeRole.isPending}
													onClick={() =>
														revokeRole.mutate(
															{
																subjectId: assignment.subjectId,
																roleId: role.id,
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
													<X className="h-4 w-4 text-destructive" />
												</Button>
											)}
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				)}
			</Panel>
		</Section>
	);
}
