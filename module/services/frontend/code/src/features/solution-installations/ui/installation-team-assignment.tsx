"use client";

import {
	article,
	type RoleAbilities,
	RoleAbilitiesList,
} from "@codefly-dev/saas-ui/solution";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import type { RoleTeam } from "../model/team-assignment";

const CONVENTIONAL = "conventional";

/**
 * Each declared role, one card each: what a holder can do (when the review
 * says), the team it goes to, and whether the person setting it up joins
 * that team. The host assigns it all when the installation is active; nothing
 * must be picked, and every choice stays editable later.
 */
export function InstallationTeamAssignment({
	assignment,
	conventional,
	teams,
	taken,
	teamsError,
	disabled,
	onChoose,
	onTake,
	abilities = [],
}: {
	assignment: readonly RoleTeam[];
	/** Each role's conventional assignment, offered back after a change. */
	conventional: readonly RoleTeam[];
	teams: readonly Team[];
	/** The roles the installer takes. */
	taken: ReadonlySet<string>;
	teamsError?: string;
	disabled: boolean;
	onChoose: (role: string, teamId: string | undefined) => void;
	onTake: (role: string, take: boolean) => void;
	/** What each role can do, from the review; a module page has none to show. */
	abilities?: readonly RoleAbilities[];
}) {
	return (
		<section aria-label="Roles" className="space-y-3">
			<div>
				<h3 className="font-medium">Roles</h3>
				<p className="text-muted-foreground">
					Each role goes to a team: everyone in the team has it. Change a
					team&apos;s people, or the team, at any time.
				</p>
			</div>
			{teamsError && (
				<p role="alert">
					Teams could not be read: {teamsError} Each role goes to its team named
					below, created if it does not exist.
				</p>
			)}
			{assignment.map((entry, i) => {
				const fallback = conventional[i];
				const items: Record<string, string> = {
					[CONVENTIONAL]: fallback.team
						? fallback.name
						: `${fallback.name} (new team)`,
				};
				for (const team of teams)
					if (team.id !== fallback.team?.id) items[team.id] = team.name;
				const can = abilities.find((a) => a.role === entry.role);
				return (
					<section
						key={entry.role}
						aria-label={`Role ${entry.role}`}
						className="space-y-2 rounded-lg border p-3"
					>
						<h4 className="font-medium">
							{can
								? `What ${article(entry.role).toLowerCase()} ${entry.role} can do`
								: `${entry.role[0]?.toUpperCase() ?? ""}${entry.role.slice(1)}`}
						</h4>
						{can && <RoleAbilitiesList abilities={can} />}
						<div className="flex flex-wrap items-center gap-2">
							<span>Goes to team</span>
							<Select
								items={items}
								value={entry.chosen ? entry.team?.id : CONVENTIONAL}
								onValueChange={(value) =>
									onChoose(
										entry.role,
										!value || value === CONVENTIONAL ? undefined : value,
									)
								}
								disabled={disabled || teams.length === 0}
							>
								<SelectTrigger
									aria-label={`Team for ${entry.role}`}
									className="w-56"
								>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{Object.entries(items).map(([value, label]) => (
										<SelectItem key={value} value={value}>
											{label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							<span className="text-muted-foreground">
								{entry.team
									? `existing team ${entry.name}`
									: `${entry.name} will be created`}
							</span>
						</div>
						<label className="inline-flex items-center gap-2">
							<input
								type="checkbox"
								checked={taken.has(entry.role)}
								disabled={disabled}
								onChange={(event) => onTake(entry.role, event.target.checked)}
							/>
							Add me to this team
						</label>
					</section>
				);
			})}
		</section>
	);
}
