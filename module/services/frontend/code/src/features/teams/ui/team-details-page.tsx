"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import { PERMISSIONS } from "@/gen/saas/accounts/v1/frontend_catalog";
import { useAuth } from "@/lib/auth";
import { hasPermission } from "@/lib/permissions";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
import { formatDate } from "@/shared/lib/utils";
import {
	Badge,
	Button,
	Page,
	PageHeader,
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";
import { roleLabel } from "../model/transforms";
import { toTeamRole } from "../model/types";
import { teamMutations } from "../service/mutations";
import { teamQueries } from "../service/queries";
import { TeamForm } from "./team-form";
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
	return (
		<Page>
			<Stack direction="row" justify="between" align="center" gap={4}>
				<Link
					href="/admin/teams"
					className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground hover:underline"
				>
					<ArrowLeft className="size-4" />
					All teams
				</Link>
				<OrgSelector />
			</Stack>
			{!orgId ? (
				<p>Select an organization to view this team.</p>
			) : quiet ? null : indicator ? (
				<Spinner label="Loading team" />
			) : isError ? (
				<div role="alert">
					{readOutcomeMessage(readOutcome(true, error), "this team", "")}{" "}
					<Button variant="outline" onClick={() => void refetch()}>
						Retry
					</Button>
				</div>
			) : !team ? (
				<div role="alert">
					<PageHeader
						title="Team unavailable"
						description="This team was deleted or isn't in your selected organization."
					/>
				</div>
			) : (
				<TeamDetails team={team} />
			)}
		</Page>
	);
}

function TeamDetails({ team }: { team: Team }) {
	const { user, orgRole, platformRole } = useAuth();
	const queryClient = useQueryClient();
	const [editing, setEditing] = useState(false);
	const members = useQuery(teamQueries.members(team.id));
	const { indicator: accessIndicator, quiet: accessQuiet } = useLoadingPhase(
		members.isPending,
	);
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
					canManage ? (
						<Button variant="outline" onClick={() => setEditing(true)}>
							Edit team
						</Button>
					) : undefined
				}
			>
				{/* Who you are here, on one line under the title, rather than a
				    bordered card of its own carrying the same weight as the roster.
				    It was three stacked lines — a chip, "You are not a team
				    administrator." and "Your platform super-admin access also allows
				    you to manage this team." — which together say one thing: whether
				    you can change anything on this page. */}
				<Stack
					direction="row"
					gap={2}
					align="center"
					className="flex-wrap pt-1 text-sm text-muted-foreground"
				>
					{accessQuiet ? null : accessIndicator ? (
						<Spinner label="Checking your team role" size="sm" />
					) : members.isError ? (
						<span>
							{readOutcomeMessage(
								readOutcome(true, members.error),
								"your role in this team",
								"",
							)}
						</span>
					) : (
						<>
							<Badge variant={teamAdmin ? "default" : "outline"}>
								{myRole
									? `Team ${roleLabel(myRole).toLowerCase()}`
									: "Not a team member"}
							</Badge>
							{/* One sentence, the most specific one that is true: a
							    super-admin does not also need telling that they hold no
							    team role, which the chip beside it already says. Each is
							    its own element so it reads as one claim, not a paragraph. */}
							<span>
								{platformRole === "super_admin"
									? "Your platform super-admin access also allows you to manage this team."
									: orgAdmin
										? "Your organization admin access allows you to manage this team."
										: teamAdmin
											? "You are a team administrator."
											: "You are not a team administrator."}
							</span>
							{!teamAdmin && !orgAdmin && platformRole !== "super_admin" && (
								<span>
									A team or organization administrator manages membership.
								</span>
							)}
							<span>·</span>
							<span>Created {formatDate(team.createdAt)}</span>
						</>
					)}
				</Stack>
			</PageHeader>
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
			{editing && (
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
							? `Couldn't update the team: ${update.error.message}`
							: undefined
					}
				/>
			)}
		</>
	);
}
