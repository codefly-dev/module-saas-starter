"use client";

import { X } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import {
	useAssignRole,
	useRevokeRole,
} from "@/features/roles/service/mutations";
import { useRoleAssignments, useRoles } from "@/features/roles/service/queries";
import { SubjectKind } from "@/gen/saas/accounts/v1/common_pb";
import {
	Badge,
	Button,
	Panel,
	Section,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Stack,
} from "@/shared/ui";

// The roles a team carries reach every member of it, which is why they belong
// on the team page rather than only on each member's.
export function TeamRoles({
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
							items={available.map((role) => ({
								value: role.id,
								label: role.name,
							}))}
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
									{roleNameById.get(assignment.roleId) ?? "Role unavailable"}
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
