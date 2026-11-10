import type { Team } from "@/gen/saas/accounts/v1/common_pb";

/**
 * The team a declared role of `target` goes to when the administrator names
 * none: `<target>-<role>`, lower case, spaces as dashes — the host's own rule.
 */
export function conventionalTeamName(target: string, role: string) {
	return `${target}-${role}`.trim().toLowerCase().replace(/\s+/g, "-");
}

/**
 * The path segment the host derives from a team's name (its `Slugify`):
 * lower case, each run of anything but a-z and 0-9 one dash, trimmed.
 */
export function teamSlug(name: string) {
	return name
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-+|-+$/g, "");
}

/**
 * Whether `team` is the organization's team named `name`, as the host finds
 * it: its name, whatever its case, or a root team whose path is the slug of
 * `name` — teams are unique by path, so a team named otherwise that holds
 * the slug is the one the host reuses.
 */
const isConventionalTeam = (team: Team, name: string) =>
	team.name.trim().toLowerCase() === name.trim().toLowerCase() ||
	(!team.parentTeamId && team.path !== "" && team.path === teamSlug(name));

/** One declared role and the team the install gives it to. */
export type RoleTeam = {
	role: string;
	/** The team's name as it will read once installed. */
	name: string;
	/** The existing team; absent when the install creates `name`. */
	team?: Team;
	/** Whether the administrator chose this team instead of the conventional one. */
	chosen: boolean;
};

/**
 * What an install assigns: for each role, the team `chosen` names, or else
 * the organization's team of the conventional name (case-insensitively, or by
 * its path), or a new team of that name, which the host creates once the
 * installation is active.
 */
export function teamAssignment(
	target: string,
	roles: readonly string[],
	teams: readonly Team[],
	chosen: Readonly<Record<string, string>>,
): RoleTeam[] {
	return roles.map((role) => {
		const picked = teams.find((team) => team.id === chosen[role]);
		if (picked) return { role, name: picked.name, team: picked, chosen: true };
		const name = conventionalTeamName(target, role);
		const team = teams.find((entry) => isConventionalTeam(entry, name));
		return { role, name: team?.name ?? name, team, chosen: false };
	});
}

/** The request's `role_teams`: every role whose team exists already. */
export function roleTeamsOf(assignment: readonly RoleTeam[]) {
	return Object.fromEntries(
		assignment.flatMap(({ role, team }) => (team ? [[role, team.id]] : [])),
	) as Record<string, string>;
}

/** The access an Install asks for beside its review. */
export type InstallAccess = {
	roleTeams: Record<string, string>;
	installerRoles: string[];
};
