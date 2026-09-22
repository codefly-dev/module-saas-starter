"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import { OrgSelector } from "@/components/org-selector";
import { useAuth } from "@/lib/auth";
import { formatDate } from "@/shared/lib/utils";
import { Badge, Button } from "@/shared/ui";
import { roleLabel } from "../model/transforms";
import { toTeamRole } from "../model/types";
import { teamQueries } from "../service/queries";
import { teamMutations } from "../service/mutations";
import { TeamForm } from "./team-form";
import { TeamMembersPanel } from "./team-members-panel";

export function TeamDetailsPage({ teamId }: { teamId: string }) {
	const { organizationId = "" } = useAuth();
	return (
		<TeamDetailsForOrganization
			key={`${organizationId}:${teamId}`}
			orgId={organizationId}
			teamId={teamId}
		/>
	);
}

function TeamDetailsForOrganization({
	orgId,
	teamId,
}: {
	orgId: string;
	teamId: string;
}) {
	const { data, isLoading, isError, refetch } = useQuery(
		teamQueries.list(orgId),
	);
	const team = data?.teams.find(
		(item) => item.id === teamId && item.orgId === orgId,
	);
	return (
		<div className="space-y-6">
			<div className="flex items-center justify-between gap-4">
				<Link
					href="/admin/teams"
					className="text-sm text-primary hover:underline"
				>
					← All teams
				</Link>
				<OrgSelector />
			</div>
			{!orgId ? (
				<p>Select an organization to view this team.</p>
			) : isLoading ? (
				<p>Loading team…</p>
			) : isError ? (
				<div role="alert">
					Couldn&apos;t load this team.{" "}
					<Button onClick={() => void refetch()}>Retry</Button>
				</div>
			) : !team ? (
				<div role="alert">
					<h1 className="text-2xl font-bold">Team unavailable</h1>
					<p>
						This team was deleted or isn&apos;t in your selected organization.
					</p>
				</div>
			) : (
				<TeamDetails team={team} />
			)}
		</div>
	);
}

function TeamDetails({ team }: { team: Team }) {
	const { user, orgRole, platformRole } = useAuth();
	const queryClient = useQueryClient();
	const [editing, setEditing] = useState(false);
	const members = useQuery(teamQueries.members(team.id));
	const membership = members.data?.members.find(
		(member) => member.userId === user?.id,
	);
	const myRole = membership ? toTeamRole(membership.role) : undefined;
	const teamAdmin = myRole === "admin" || myRole === "owner";
	const orgAdmin = orgRole === "admin" || orgRole === "owner";
	const canManage =
		!members.isError &&
		!members.isPending &&
		(teamAdmin || orgAdmin || platformRole === "super_admin");
	const update = useMutation({
		mutationFn: (values: { name: string; description?: string }) =>
			teamMutations.update(team.id, values.name, values.description),
		onSuccess: async () => {
			await queryClient.invalidateQueries({ queryKey: ["teams", team.orgId] });
			setEditing(false);
			toast.success("Team updated");
		},
	});
	return (
		<>
			<div className="flex items-start justify-between gap-4">
				<div className="space-y-2">
					<h1 className="text-2xl font-bold">{team.name}</h1>
					<p className="text-muted-foreground">
						{team.description || "No description yet."}
					</p>
					<p className="text-sm text-muted-foreground">
						Created {formatDate(team.createdAt)}
					</p>
				</div>
				{canManage && (
					<Button variant="outline" onClick={() => setEditing(true)}>
						Edit team
					</Button>
				)}
			</div>
			<section
				aria-label="Your team access"
				className="space-y-2 rounded-lg border p-4"
			>
				<h2 className="font-semibold">Your access</h2>
				{members.isPending ? (
					<p>Checking your team role…</p>
				) : members.isError ? (
					<p>Unable to verify your team role.</p>
				) : (
					<>
						<Badge variant={teamAdmin ? "default" : "outline"}>
							{myRole
								? `Team ${roleLabel(myRole).toLowerCase()}`
								: "Not a team member"}
						</Badge>
						<p>
							{teamAdmin
								? "You are a team administrator."
								: "You are not a team administrator."}
						</p>
						<p className="text-sm text-muted-foreground">
							{platformRole === "super_admin"
								? "Your platform super-admin access also allows you to manage this team."
								: orgAdmin
									? "Your organization admin access allows you to manage this team."
									: teamAdmin
										? "You can edit this team, add members, change member roles, and remove members."
										: "You can view the roster. A team or organization administrator manages membership."}
						</p>
					</>
				)}
			</section>
			<TeamMembersPanel
				orgId={team.orgId}
				teamId={team.id}
				teamName={team.name}
				canManage={canManage}
				currentUserId={user?.id}
			/>
			{editing && (
				<>
					<TeamForm
						open
						mode="edit"
						initial={{ name: team.name, description: team.description }}
						onSubmit={(values) => update.mutate(values)}
						onCancel={() => {
							if (!update.isPending) {
								setEditing(false);
								update.reset();
							}
						}}
						isPending={update.isPending}
						error={update.isError ? `Couldn't update the team: ${update.error.message}` : undefined}
					/>
				</>
			)}
		</>
	);
}
