"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { type ReactNode, useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import { ActAsTeam } from "@/features/role-view/role-view";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { failureText } from "@/shared/lib/failure-text";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	ErrorState,
	Page as PageBody,
	PageHeader,
	Panel,
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";
import { roleLabel } from "../model/transforms";
import { toTeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";
import { TeamForm } from "./team-form";
import { TeamInstallationAccess } from "./team-installation-access";
import { TeamMembersPanel } from "./team-members-panel";
import { TeamRoles } from "./team-roles";

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
	const { data, isLoading, isError, error, refetch } = useQuery(
		teamQueries.list(orgId),
	);
	const { indicator, quiet } = useLoadingPhase(isLoading);
	const team = data?.teams.find(
		(item) => item.id === teamId && item.orgId === orgId,
	);
	const outcome = readOutcome(isError, error);
	if (!orgId) {
		return (
			<PageBody>
				<PageHeader
					title="Team"
					description="Select an organization to view this team."
					actions={<TeamHeaderActions />}
				/>
			</PageBody>
		);
	}
	// `quiet` renders nothing rather than falling through to "Team unavailable",
	// which would flash over every load of a team that exists.
	if (quiet) return null;
	if (indicator) {
		return (
			<PageBody>
				<Spinner label="Loading team" />
			</PageBody>
		);
	}
	if (isError) {
		return (
			<PageBody>
				<PageHeader title="Team" actions={<TeamHeaderActions />} />
				<Panel>
					<Stack gap={3} align="start">
						<ErrorState
							title="Couldn't load this team"
							detail={
								outcome === "forbidden"
									? "You don't have permission to see this organization's teams. Ask one of its administrators for access."
									: "Try again; if it keeps failing, tell your administrator."
							}
						/>
						{/* A refusal is not cured by asking again. */}
						{outcome === "failed" && (
							<Button
								variant="outline"
								size="sm"
								onClick={() => void refetch()}
							>
								Try again
							</Button>
						)}
					</Stack>
				</Panel>
			</PageBody>
		);
	}
	if (!team) {
		return (
			<PageBody>
				<PageHeader
					role="alert"
					title="Team unavailable"
					description="This team was deleted or isn't in your selected organization."
					actions={<TeamHeaderActions />}
				/>
			</PageBody>
		);
	}
	return (
		<PageBody>
			<TeamDetails team={team} />
		</PageBody>
	);
}

// The organization selector and the way back, on every state of the page.
function TeamHeaderActions({ children }: { children?: ReactNode }) {
	return (
		<>
			<OrgSelector />
			<Button
				variant="outline"
				size="sm"
				nativeButton={false}
				render={<Link href="/admin/teams" />}
			>
				<ArrowLeft className="mr-1 h-4 w-4" />
				All teams
			</Button>
			{children}
		</>
	);
}

function TeamDetails({ team }: { team: Team }) {
	const { user, orgRole, platformRole, castTeamId, sessionRead } = useAuth();
	const unconfirmed = sessionRead === "reading" || sessionRead === "failed";
	const queryClient = useQueryClient();
	const [editing, setEditing] = useState(false);
	const members = useQuery(teamQueries.members(team.id));
	const { indicator: accessIndicator, quiet: accessQuiet } = useLoadingPhase(
		members.isPending,
	);
	const membership = members.data?.members.find(
		(member) => member.userId === user?.id,
	);
	// A cast holds what its team holds, not the person's own team role.
	const myRole =
		membership && !castTeamId && !unconfirmed
			? toTeamRole(membership.role)
			: undefined;
	const teamAdmin = myRole === "admin" || myRole === "owner";
	const orgAdmin = orgRole === "admin" || orgRole === "owner";
	const canManage =
		!members.isError &&
		!members.isPending &&
		(teamAdmin || orgAdmin || platformRole === "super_admin");
	const update = useMutation({
		mutationFn: (values: { name: string; description?: string }) =>
			teamMutations.update(team.id, values.name, values.description),
		// Closed on the write, not on the refetch that follows it — an awaited
		// invalidation keeps the mutation `isPending`, holding this dialog open on
		// "Saving…" while the teams list reloads. Same shape as #964's add button.
		onSuccess: () => {
			setEditing(false);
			toast.success("Team updated");
			void queryClient.invalidateQueries({ queryKey: ["teams", team.orgId] });
		},
	});
	return (
		<>
			<PageHeader
				title={team.name}
				description={team.description || "No description yet."}
				actions={
					<TeamHeaderActions>
						{canManage && (
							<Button variant="outline" onClick={() => setEditing(true)}>
								Edit team
							</Button>
						)}
					</TeamHeaderActions>
				}
			>
				<p className="text-sm text-muted-foreground">
					Created {formatDate(team.createdAt)}
				</p>
			</PageHeader>
			<Panel role="region" aria-label="Your team access" className="space-y-2">
				<h2 className="font-semibold">Your access</h2>
				{accessQuiet ? null : accessIndicator ? (
					<Spinner label="Checking your team role" />
				) : members.isError ? (
					<p>
						{readOutcomeMessage(
							readOutcome(true, members.error),
							"your role in this team",
							"",
						)}
					</p>
				) : unconfirmed ? (
					<p>
						{sessionRead === "reading"
							? "Confirming what this session is."
							: "Could not confirm what this session is."}{" "}
						Your access here is not shown until the host says; it decides every
						action.
					</p>
				) : castTeamId ? (
					<>
						<Badge variant="outline">
							{castTeamId === team.id
								? "Viewing as this team"
								: "Viewing as another team"}
						</Badge>
						<p className="text-sm text-muted-foreground">
							This session holds what the team it acts as holds and nothing of
							your own: not your team role, not your administrator access. The
							host decides what it may do here and refuses the rest by name.
						</p>
					</>
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
			</Panel>
			<ActAsTeam teamId={team.id} teamName={team.name} />
			<TeamMembersPanel
				orgId={team.orgId}
				teamId={team.id}
				teamName={team.name}
				canManage={canManage}
				currentUserId={user?.id}
			/>
			<TeamRoles
				teamId={team.id}
				orgId={team.orgId}
				canGrant={hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE)}
			/>
			{/* Reading an organization's installations is an administrator's
			    read (a member's own is ListHeld), and the links go to admin pages:
			    a member's team page leaves the section out rather than refuse it. */}
			{hasPermission(platformRole, orgRole, PERMISSIONS.ROLES_WRITE) && (
				<TeamInstallationAccess teamId={team.id} orgId={team.orgId} />
			)}
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
						error={
							update.isError
								? failureText("update the team", update.error)
								: undefined
						}
					/>
				</>
			)}
		</>
	);
}
