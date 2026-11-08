"use client";

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import type { AvailableSolution } from "@/gen/saas/accounts/v1/installations_pb";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/shared/ui";
import { hostRefusal } from "../model/installations";
import {
	type InstallAccess,
	roleTeamsOf,
	teamAssignment,
} from "../model/team-assignment";
import { InstallationTeamAssignment } from "./installation-team-assignment";

/**
 * Set up who may use a module page, in one act: a module page is installed by
 * composition, never by an administrator, and arrives with no access. Each of
 * its roles goes to a team (the conventional `<module>-<role>` one unless
 * changed) and the administrator takes the ticked roles, exactly as a
 * solution's install assigns them; the host applies it on the page's
 * composition installation.
 */
export function SetUpModulePageDialog({
	orgId,
	page,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	page: AvailableSolution;
	onSubmit: (access: InstallAccess) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const [chosen, setChosen] = useState<Record<string, string>>({});
	const [untaken, setUntaken] = useState<ReadonlySet<string>>(new Set());
	const teams = useQuery({
		...teamQueries.list(orgId),
		enabled: Boolean(orgId),
	});
	const teamList = teams.data?.teams ?? [];
	const target = page.routeAlias;
	const assignment = teamAssignment(target, page.roles, teamList, chosen);
	const access: InstallAccess = {
		roleTeams: roleTeamsOf(assignment),
		installerRoles: page.roles.filter((role) => !untaken.has(role)),
	};
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-[640px]">
				<DialogHeader>
					<DialogTitle>Set up access to {target}</DialogTitle>
					<DialogDescription>
						{target} is a module page: it is part of the platform, installed for
						this organization, and nobody can use it until its roles are given
						to teams.
					</DialogDescription>
				</DialogHeader>
				<InstallationTeamAssignment
					assignment={assignment}
					conventional={teamAssignment(target, page.roles, teamList, {})}
					teams={teamList}
					taken={new Set(access.installerRoles)}
					teamsError={teams.isError ? hostRefusal(teams.error) : undefined}
					disabled={isPending}
					onChoose={(role, teamId) =>
						setChosen(({ [role]: _, ...rest }) =>
							teamId ? { ...rest, [role]: teamId } : rest,
						)
					}
					onTake={(role, take) =>
						setUntaken((current) => {
							const next = new Set(current);
							if (take) next.delete(role);
							else next.add(role);
							return next;
						})
					}
				/>
				{error && <p role="alert">{error}</p>}
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button disabled={isPending} onClick={() => onSubmit(access)}>
						{isPending ? "Setting up…" : "Set up access"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
