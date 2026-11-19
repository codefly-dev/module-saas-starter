"use client";

import { useQuery } from "@tanstack/react-query";
import {
	createColumnHelper,
	getCoreRowModel,
	getPaginationRowModel,
	useReactTable,
} from "@tanstack/react-table";
import { ArrowLeft, Save } from "lucide-react";
import Link from "next/link";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import { UserLabel } from "@/components/user-label";
import { activityLabel } from "@/features/permissions/model/activity-label";
import { permissionLabel } from "@/features/permissions/model/effective";
import { permissionQueries } from "@/features/permissions/service/queries";
import { teamQueries } from "@/features/teams/service/queries";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { failureText } from "@/shared/lib/failure-text";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Badge,
	Button,
	Checkbox,
	ErrorState,
	Field,
	Input,
	Label,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";
import { DataTable } from "@/shared/ui/data-table";
import type { Permission, Role, RoleAssignment } from "../model/types";
import { useUpdateRole } from "../service/mutations";
import { useRoleAssignments, useRoles } from "../service/queries";
import { RevokeRoleButton } from "./revoke-role-button";

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
	const { data: roles = [], isLoading, isError, error } = useRoles(orgId);
	const role = (roles as Role[]).find((candidate) => candidate.id === roleId);
	const { indicator, quiet } = useLoadingPhase(isLoading);

	// `quiet` renders nothing rather than falling through: the next branch is
	// "Role not found", which would flash over every load of a role that exists.
	if (quiet) return null;
	if (indicator) {
		return (
			<PageBody>
				<Spinner label="Loading role" />
			</PageBody>
		);
	}
	// "Role not found" is a statement about the organization's roles, so it is
	// only ever made about a list that was actually read. A refused or failed
	// read defaults `roles` to [] exactly as a genuinely absent id does, and
	// saying "not found" there sends an administrator looking for a deleted role
	// instead of at the grant or the outage that is really in their way.
	if (isError) {
		return (
			<PageBody>
				<PageHeader
					title="This role can't be shown"
					description={readOutcomeMessage(
						readOutcome(true, error),
						"this organization's roles",
						"",
					)}
					actions={<OrgSelector />}
				/>
				<BackToRoles />
			</PageBody>
		);
	}
	if (!role) {
		return (
			<PageBody>
				<PageHeader
					title="Role not found"
					description="This role is not available in the selected organization."
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
	const catalogue = useQuery(permissionQueries.serviceInfo());
	const info = catalogue.data;

	// The vocabulary the service declares, plus anything this role already
	// holds — a grant the service no longer declares still has to be visible,
	// or saving would quietly drop it.
	const declared = (info?.capabilities?.permissions ?? []).map((permission) =>
		permissionLabel(permission),
	);
	const vocabulary = [...new Set([...declared, ...granted])];
	const activities = vocabulary.map((key) => {
		const permission = info?.capabilities?.permissions.find(
			(entry) => permissionLabel(entry) === key,
		);
		return {
			key,
			description: permission ? activityLabel(permission) : undefined,
		};
	});

	const toggle = (label: string) => {
		setGranted((current) => {
			const next = new Set(current);
			if (!next.delete(label)) next.add(label);
			return next;
		});
	};

	const save = () => {
		if (
			!canWrite ||
			updateRole.isPending ||
			catalogue.isPending ||
			catalogue.isError
		)
			return;
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
				onError: (error) => toast.error(failureText("update the role", error)),
			},
		);
	};

	return (
		<Section
			title="What this role allows"
			description={
				canWrite
					? "Choose the activities available to people and teams with this role."
					: "You can review this role’s access here. This role cannot be edited by your account."
			}
			actions={
				canWrite ? (
					<Button
						size="sm"
						onClick={save}
						disabled={
							updateRole.isPending || catalogue.isPending || catalogue.isError
						}
					>
						<Save className="mr-1 h-4 w-4" />
						{updateRole.isPending ? "Saving…" : "Save changes"}
					</Button>
				) : undefined
			}
		>
			<Panel>
				<Stack gap={4}>
					<Field label="Description">
						{(control) => (
							<Input
								{...control}
								value={description}
								disabled={!canWrite || updateRole.isPending}
								onChange={(event) => setDescription(event.target.value)}
								placeholder="What this role is for"
							/>
						)}
					</Field>
					{updateRole.isError && (
						<ErrorState
							title="Couldn't update the role"
							detail={failureText("update the role", updateRole.error)}
						/>
					)}
					{catalogue.isError ? (
						<ErrorState
							title="Activities unavailable"
							detail="Couldn’t load the available activities. Your changes have not been saved. Ask your organization administrator to check your access."
						/>
					) : catalogue.isPending ? (
						<Spinner label="Loading available activities" />
					) : (
						<div className="grid gap-3 md:grid-cols-2">
							{activities.map(({ key, description: activity }) => (
								<div
									key={key}
									className="flex items-start gap-2 rounded-lg border p-3"
								>
									<Checkbox
										id={`role-activity-${key}`}
										checked={granted.has(key)}
										disabled={!canWrite || updateRole.isPending || !activity}
										onCheckedChange={() => toggle(key)}
									/>
									<Label
										htmlFor={`role-activity-${key}`}
										className="leading-relaxed"
									>
										{activity ||
											"Saved access without a description. It will be preserved when you save."}
									</Label>
								</div>
							))}
						</div>
					)}
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
	const {
		data: assignments = [],
		isLoading,
		isError,
		error,
	} = useRoleAssignments(orgId, undefined, SubjectKind.UNSPECIFIED);
	const { indicator, quiet } = useLoadingPhase(isLoading);
	const { data: teams } = useQuery(teamQueries.list(orgId));
	const outcome = readOutcome(isError, error);
	// A refused read takes precedence over rows already on screen. TanStack keeps
	// the last successful answer when a refetch rejects, so testing `length === 0`
	// first would leave the previous holders rendered after the read was refused —
	// the same wrong answer as "nobody holds this role", reached from the other
	// side. A transient failure keeps them: blanking a good list because one
	// request did not come back is worse than saying it may be out of date.
	const withheld = !mayKeepRetainedRows(outcome);
	const stale = staleReadNotice(outcome, "who holds this role");

	const teamNameById = new Map(
		(teams?.teams ?? []).map((team) => [team.id, team.name] as const),
	);
	const holders = withheld
		? []
		: assignments.filter((assignment) => assignment.roleId === role.id);
	// A disabled query is not a loading one: with no organization selected
	// `useRoleAssignments` never runs, `isLoading` is false, and the render would
	// otherwise fall through to a confident "Nobody holds this role" about a read
	// that was never issued.
	const noOrganization = !orgId;

	return (
		<Section
			title="Held by"
			description="People and teams assigned this role in the organization."
		>
			<Panel>
				{quiet ? null : indicator ? (
					<Spinner label="Loading who holds this role" size="sm" />
				) : noOrganization ? (
					<span className="text-sm text-muted-foreground">
						Select an organization to see who holds this role.
					</span>
				) : holders.length === 0 ? (
					// "Nobody holds this role" is a conclusion an administrator acts on,
					// so it is only ever said about a read that succeeded. A denied or
					// failed read says so instead of borrowing that sentence.
					<span
						className="text-sm text-muted-foreground"
						{...(isError ? { role: "alert" as const } : {})}
					>
						{readOutcomeMessage(
							outcome,
							"who holds this role",
							"Nobody holds this role.",
						)}
					</span>
				) : (
					<>
						{stale && (
							<p role="status" className="text-sm text-muted-foreground">
								{stale}
							</p>
						)}
						<RoleHolderTable
							holders={holders}
							teamNameById={teamNameById}
							role={role}
							orgId={orgId}
							canWrite={canWrite}
						/>
					</>
				)}
			</Panel>
		</Section>
	);
}

const holderColumn = createColumnHelper<RoleAssignment>();
function RoleHolderTable({
	holders,
	teamNameById,
	role,
	orgId,
	canWrite,
}: {
	holders: RoleAssignment[];
	teamNameById: Map<string, string>;
	role: Role;
	orgId: string;
	canWrite: boolean;
}) {
	const columns = useMemo(
		() => [
			holderColumn.accessor("subjectId", {
				header: "Person or team",
				cell: ({ row }) => {
					const holder = row.original;
					const isTeam = holder.subjectKind === SubjectKind.TEAM;
					return (
						<Link
							href={`/admin/${isTeam ? "teams" : "users"}/${holder.subjectId}`}
							className="text-primary hover:underline"
						>
							{isTeam ? (
								teamNameById.get(holder.subjectId) || "Team unavailable"
							) : (
								<UserLabel userId={holder.subjectId} />
							)}
						</Link>
					);
				},
			}),
			holderColumn.accessor("subjectKind", {
				header: "Type",
				cell: ({ getValue }) => (
					<Badge variant="outline">
						{getValue() === SubjectKind.TEAM ? "Team" : "Person"}
					</Badge>
				),
			}),
			holderColumn.accessor("scope", {
				header: "Applies to",
				cell: ({ getValue }) =>
					getValue() ? "Selected items" : "Organization",
			}),
			holderColumn.display({
				id: "actions",
				header: "",
				cell: ({ row }) =>
					canWrite ? (
						<RevokeRoleButton
							subjectId={row.original.subjectId}
							subjectLabel={
								row.original.subjectKind === SubjectKind.TEAM ? (
									teamNameById.get(row.original.subjectId) || "this team"
								) : (
									<UserLabel userId={row.original.subjectId} />
								)
							}
							roleId={role.id}
							roleName={role.name}
							orgId={orgId}
							scope={row.original.scope}
						/>
					) : null,
			}),
		],
		[canWrite, orgId, role.id, role.name, teamNameById],
	);
	const table = useReactTable({
		data: holders,
		columns,
		getCoreRowModel: getCoreRowModel(),
		getPaginationRowModel: getPaginationRowModel(),
		initialState: { pagination: { pageSize: 10 } },
	});
	return <DataTable table={table} />;
}
