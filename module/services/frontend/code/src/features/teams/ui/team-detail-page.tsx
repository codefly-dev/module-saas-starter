"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, UserPlus, X } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import {
	useAssignRole,
	useRevokeRole,
} from "@/features/roles/service/mutations";
import { useRoleAssignments, useRoles } from "@/features/roles/service/queries";
import { SubjectKind, TeamRole } from "@/gen/saas/accounts/v1/common_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { truncateUUID } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Input,
	Page as PageBody,
	PageHeader,
	Panel,
	Section,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Stack,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";

export function TeamDetailPage({ teamId }: { teamId: string }) {
	const { organizationId: orgId = "", platformRole, orgRole } = useAuth();
	return (
		<TeamDetail
			key={orgId}
			teamId={teamId}
			orgId={orgId}
			canWrite={hasPermission(platformRole, orgRole, PERMISSIONS.TEAMS_WRITE)}
			canGrant={hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)}
		/>
	);
}

function TeamDetail({
	teamId,
	orgId,
	canWrite,
	canGrant,
}: {
	teamId: string;
	orgId: string;
	canWrite: boolean;
	canGrant: boolean;
}) {
	const { data: teams, isLoading } = useQuery(teamQueries.list(orgId));
	const team = (teams?.teams ?? []).find(
		(candidate) => candidate.id === teamId,
	);

	if (isLoading) {
		return <PageBody>Loading…</PageBody>;
	}
	if (!team) {
		return (
			<PageBody>
				<PageHeader
					title="Team not found"
					description="No team with this id is visible in the selected organization."
					actions={<OrgSelector />}
				/>
				<BackToTeams />
			</PageBody>
		);
	}

	return (
		<PageBody>
			<PageHeader
				title={team.name}
				description={team.description || "No description."}
				actions={
					<Stack direction="row" gap={2} align="center">
						<OrgSelector />
						<BackToTeams />
					</Stack>
				}
			>
				<span className="select-all font-mono text-xs text-muted-foreground">
					{team.path || team.id}
				</span>
			</PageHeader>

			<TeamMembers teamId={teamId} orgId={orgId} canWrite={canWrite} />
			<TeamRoles teamId={teamId} orgId={orgId} canGrant={canGrant} />
		</PageBody>
	);
}

function BackToTeams() {
	return (
		<Button
			variant="outline"
			size="sm"
			nativeButton={false}
			render={<Link href="/admin/teams" />}
		>
			<ArrowLeft className="mr-1 h-4 w-4" />
			All teams
		</Button>
	);
}

function TeamMembers({
	teamId,
	orgId,
	canWrite,
}: {
	teamId: string;
	orgId: string;
	canWrite: boolean;
}) {
	const queryClient = useQueryClient();
	const [userId, setUserId] = useState("");
	const { data: members, isLoading } = useQuery(teamQueries.members(teamId));

	const invalidate = () => {
		queryClient.invalidateQueries({ queryKey: ["team-members", teamId] });
		// A membership change moves which teams the member's own page lists.
		queryClient.invalidateQueries({ queryKey: ["teams", orgId] });
	};

	const add = useMutation({
		mutationFn: () =>
			teamMutations.addMember(teamId, userId.trim(), TeamRole.MEMBER),
		onSuccess: () => {
			toast.success("Member added");
			setUserId("");
			invalidate();
		},
		onError: (error) =>
			toast.error(`Failed to add member: ${(error as Error).message}`),
	});

	const remove = useMutation({
		mutationFn: (member: string) => teamMutations.removeMember(teamId, member),
		onSuccess: () => {
			toast.success("Member removed");
			invalidate();
		},
		onError: (error) =>
			toast.error(`Failed to remove member: ${(error as Error).message}`),
	});

	return (
		<Section
			title="Members"
			description="A team membership is a child of an organization membership: the target must already be in this organization."
			actions={
				canWrite ? (
					<Stack direction="row" gap={2} align="center">
						<Input
							value={userId}
							onChange={(event) => setUserId(event.target.value)}
							placeholder="User ID"
							className="w-72 font-mono text-xs"
						/>
						<Button
							size="sm"
							disabled={!userId.trim() || add.isPending}
							onClick={() => add.mutate()}
						>
							<UserPlus className="mr-1 h-4 w-4" />
							Add
						</Button>
					</Stack>
				) : undefined
			}
		>
			<Panel>
				{isLoading ? (
					<span className="text-sm text-muted-foreground">Loading…</span>
				) : (members?.members ?? []).length === 0 ? (
					<span className="text-sm text-muted-foreground">
						This team has no members.
					</span>
				) : (
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>User</TableHead>
								<TableHead>Team role</TableHead>
								<TableHead />
							</TableRow>
						</TableHeader>
						<TableBody>
							{(members?.members ?? []).map((member) => (
								<TableRow key={member.userId}>
									<TableCell>
										<Link
											href={`/admin/users/${member.userId}`}
											className="font-mono text-xs text-primary hover:underline"
										>
											{truncateUUID(member.userId)}
										</Link>
									</TableCell>
									<TableCell>
										<Badge variant="outline">{TeamRole[member.role]}</Badge>
									</TableCell>
									<TableCell>
										{canWrite && (
											<Button
												variant="ghost"
												size="sm"
												disabled={remove.isPending}
												onClick={() => remove.mutate(member.userId)}
											>
												<X className="h-4 w-4 text-destructive" />
											</Button>
										)}
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				)}
			</Panel>
		</Section>
	);
}

// The roles a team carries reach every member of it, which is why they belong
// on the team page rather than only on each member's.
function TeamRoles({
	teamId,
	orgId,
	canGrant,
}: {
	teamId: string;
	orgId: string;
	canGrant: boolean;
}) {
	const [picked, setPicked] = useState("");
	const { data: roles = [] } = useRoles(orgId);
	const { data: assignments = [], isLoading } = useRoleAssignments(
		orgId,
		teamId,
		SubjectKind.TEAM,
	);
	const assignRole = useAssignRole();
	const revokeRole = useRevokeRole();

	const roleNameById = new Map(
		roles.map((role) => [role.id, role.name] as const),
	);
	const held = new Set(assignments.map((assignment) => assignment.roleId));
	const available = roles.filter((role) => !held.has(role.id));

	return (
		<Section
			title="Roles"
			description="Every member of this team holds these roles."
			actions={
				canGrant ? (
					<Stack direction="row" gap={2} align="center">
						<Select
							value={picked}
							onValueChange={(value) => setPicked(value ?? "")}
						>
							<SelectTrigger className="w-56">
								<SelectValue placeholder="Pick a role…" />
							</SelectTrigger>
							<SelectContent>
								{available.map((role) => (
									<SelectItem key={role.id} value={role.id}>
										{role.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<Button
							size="sm"
							disabled={!picked || assignRole.isPending}
							onClick={() =>
								assignRole.mutate(
									{
										subjectId: teamId,
										subjectKind: SubjectKind.TEAM,
										roleId: picked,
										orgId,
									},
									{
										onSuccess: () => {
											toast.success("Role granted to team");
											setPicked("");
										},
										onError: (error) =>
											toast.error(
												`Failed to grant role: ${(error as Error).message}`,
											),
									},
								)
							}
						>
							Grant
						</Button>
					</Stack>
				) : undefined
			}
		>
			<Panel>
				{isLoading ? (
					<span className="text-sm text-muted-foreground">Loading…</span>
				) : assignments.length === 0 ? (
					<span className="text-sm text-muted-foreground">
						This team carries no roles.
					</span>
				) : (
					<Stack direction="row" gap={2} className="flex-wrap">
						{assignments.map((assignment) => (
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
													subjectId: teamId,
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
	);
}
