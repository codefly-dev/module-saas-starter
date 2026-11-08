"use client";

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
 * Who gets access when the install is active, assigned by the host: each
 * declared role goes to a team, and the installer takes the ticked roles by
 * joining their teams. Nothing must be picked; every choice stays editable
 * later in Set up.
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
}) {
	return (
		<section aria-label="Team access" className="space-y-3">
			<h3 className="font-medium">Team access</h3>
			<p>
				You will get the ticked roles; others get access by joining a team, here
				or in Set up.
			</p>
			<fieldset className="space-y-1" disabled={disabled}>
				<legend className="font-medium">You will get</legend>
				{assignment.map(({ role }) => (
					<label key={role} className="mr-4 inline-flex items-center gap-2">
						<input
							type="checkbox"
							checked={taken.has(role)}
							onChange={(event) => onTake(role, event.target.checked)}
						/>
						{role}
					</label>
				))}
			</fieldset>
			{teamsError && (
				<p role="alert">
					Teams could not be read: {teamsError} Each role goes to its team named
					below, created if it does not exist.
				</p>
			)}
			<ul aria-label="Team for each role" className="space-y-2">
				{assignment.map((entry, i) => {
					const fallback = conventional[i];
					const items: Record<string, string> = {
						[CONVENTIONAL]: fallback.team
							? fallback.name
							: `${fallback.name} (new)`,
					};
					for (const team of teams)
						if (team.id !== fallback.team?.id) items[team.id] = team.name;
					return (
						<li key={entry.role} className="flex flex-wrap items-center gap-2">
							<span className="min-w-0 flex-1">
								<span className="font-medium">{entry.role}</span>:{" "}
								{entry.team
									? `existing team ${entry.name}`
									: `${entry.name} (will be created)`}
							</span>
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
						</li>
					);
				})}
			</ul>
		</section>
	);
}
