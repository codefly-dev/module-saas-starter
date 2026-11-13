"use client";

import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
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
	roles = page.roles,
	onSubmit,
	isPending,
	error,
	onClose,
}: {
	orgId: string;
	page: AvailableSolution;
	/**
	 * The roles this Set up gives teams: every role of a module page, or the
	 * roles of an installed solution that nobody holds.
	 */
	roles?: string[];
	onSubmit: (access: InstallAccess) => void;
	isPending: boolean;
	error?: string;
	onClose: () => void;
}) {
	const top = useRef<HTMLDivElement>(null);
	const [chosen, setChosen] = useState<Record<string, string>>({});
	const [untaken, setUntaken] = useState<ReadonlySet<string>>(new Set());
	const teams = useQuery({
		...teamQueries.list(orgId),
		enabled: Boolean(orgId),
	});
	const teamList = teams.data?.teams ?? [];
	const target = page.routeAlias;
	const assignment = teamAssignment(target, roles, teamList, chosen);
	const access: InstallAccess = {
		roleTeams: roleTeamsOf(assignment),
		installerRoles: roles.filter((role) => !untaken.has(role)),
	};
	return (
		<Dialog open onOpenChange={(open) => !open && onClose()}>
			{/* Focus lands on the dialog itself, never on a control further
			    down, so a short screen opens it at its top. */}
			<DialogContent
				ref={top}
				initialFocus={top}
				className="max-h-[85vh] overflow-y-auto sm:max-w-[640px]"
			>
				<DialogHeader>
					<DialogTitle>Set up access to {target}</DialogTitle>
					<DialogDescription>
						{page.kind === "module"
							? `${target} is a module page: it is part of the platform, installed for this organization, and nobody can use it until its roles are given to teams.`
							: `${target} is installed, and nobody holds ${roles.join(", ")} yet. Each goes to a team; the roles people hold already stay as they are.`}
					</DialogDescription>
				</DialogHeader>
				<InstallationTeamAssignment
					assignment={assignment}
					conventional={teamAssignment(target, roles, teamList, {})}
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
