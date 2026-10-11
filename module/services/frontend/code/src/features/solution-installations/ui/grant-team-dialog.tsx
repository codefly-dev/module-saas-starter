"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { useRoles } from "@/features/roles/service/queries";
import { teamQueries } from "@/features/teams/service/queries";
import { readOutcome, readOutcomeMessage } from "@/shared/lib/read-outcome";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import { hostRefusal, roleOptionLabel } from "../model/installations";
import { useGrantInstallationToTeam } from "../service/mutations";

/**
 * Exposes one installation to one team. The administrator names the team and
 * the role it holds there; the host resolves where the grant is written, and
 * refuses a role that would expose nothing — in its own words, shown here.
 */
export function GrantTeamDialog({
	orgId,
	installationId,
	solution,
	onClose,
}: {
	orgId: string;
	installationId: string;
	solution: string;
	onClose: () => void;
}) {
	const grant = useGrantInstallationToTeam(orgId);
	const teams = useQuery(teamQueries.list(orgId));
	const roles = useRoles(orgId);
	const [teamId, setTeamId] = useState("");
	const [roleId, setRoleId] = useState("");

	const teamList = teams.data?.teams ?? [];
	const roleList = roles.data ?? [];
	const teamName = new Map(teamList.map((t) => [t.id, t.name] as const));
	const roleLabel = new Map(
		roleList.map((r) => [r.id, roleOptionLabel(r)] as const),
	);

	const submit = () =>
		grant.mutate(
			{ installationId, teamId, roleId },
			{
				onSuccess: () => {
					toast.success(
						`${teamName.get(teamId) ?? "Team"} can now use ${solution}`,
					);
					onClose();
				},
			},
		);

	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="sm:max-w-[480px]">
				<DialogHeader>
					<DialogTitle>Grant a team access to {solution}</DialogTitle>
					<DialogDescription>
						Members of the team see and use the solution. Other members of the
						organization do not.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-2">
						<Label htmlFor="grant-team">Team</Label>
						<Select value={teamId} onValueChange={(v) => setTeamId(v ?? "")}>
							<SelectTrigger
								id="grant-team"
								aria-label="Team"
								className="w-full"
							>
								<SelectValue placeholder="Choose a team…">
									{teamId
										? teamName.get(teamId) || "Team unavailable"
										: undefined}
								</SelectValue>
							</SelectTrigger>
							<SelectContent>
								{teamList.length === 0 ? (
									<div className="px-2 py-2 text-sm text-muted-foreground">
										{teams.isLoading
											? "Loading teams…"
											: readOutcomeMessage(
													readOutcome(teams.isError, teams.error),
													"this organization's teams",
													"This organization has no team yet.",
												)}
									</div>
								) : (
									teamList.map((team) => (
										<SelectItem key={team.id} value={team.id}>
											{team.name}
										</SelectItem>
									))
								)}
							</SelectContent>
						</Select>
					</div>
					<div className="space-y-2">
						<Label htmlFor="grant-role">Role</Label>
						<Select value={roleId} onValueChange={(v) => setRoleId(v ?? "")}>
							<SelectTrigger
								id="grant-role"
								aria-label="Role"
								aria-describedby="grant-role-help"
								className="w-full"
							>
								<SelectValue placeholder="Choose a role…">
									{roleId
										? roleLabel.get(roleId) || "Role unavailable"
										: undefined}
								</SelectValue>
							</SelectTrigger>
							<SelectContent>
								{roleList.length === 0 ? (
									<div className="px-2 py-2 text-sm text-muted-foreground">
										{roles.isLoading
											? "Loading roles…"
											: readOutcomeMessage(
													readOutcome(roles.isError, roles.error),
													"this organization's roles",
													"No roles available.",
												)}
									</div>
								) : (
									roleList.map((role) => (
										<SelectItem key={role.id} value={role.id}>
											{roleOptionLabel(role)}
										</SelectItem>
									))
								)}
							</SelectContent>
						</Select>
						<p id="grant-role-help" className="text-xs text-muted-foreground">
							What the team may do inside the solution. The role must permit
							using a solution, or the host refuses the grant.
						</p>
					</div>
					{grant.isError && (
						<p role="alert" className="text-sm text-destructive">
							The host refused the grant: {hostRefusal(grant.error)}
						</p>
					)}
				</div>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button
						type="button"
						disabled={!teamId || !roleId || grant.isPending}
						onClick={submit}
					>
						{grant.isPending ? "Granting…" : "Grant"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
