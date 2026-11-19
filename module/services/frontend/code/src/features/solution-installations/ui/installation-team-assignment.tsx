"use client";

import {
	article,
	type RoleAbilities,
	RoleAbilitiesList,
	roleLabel,
} from "@codefly-dev/saas-ui/solution";
import { useId } from "react";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import {
	Checkbox,
	ErrorState,
	Field,
	Label,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/ui";
import type { RoleTeam } from "../model/team-assignment";
import { ReviewSection, RolePanel } from "./review-section";

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
	title,
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
	/** How the host titles the target; each role reads as its ("<Title> admin"). */
	title?: string;
}) {
	const checkboxPrefix = useId();
	return (
		<ReviewSection
			title="Roles"
			className="space-y-3"
			description="Each role goes to a team: everyone in the team has it. Change a team's people, or the team, at any time."
		>
			{teamsError && (
				<ErrorState
					title="Teams could not be read"
					detail={`${teamsError} Each role goes to its team named below, created if it does not exist.`}
				/>
			)}
			{assignment.map((entry, i) => (
				<RoleAssignment
					key={entry.role}
					entry={entry}
					fallback={conventional[i]}
					teams={teams}
					taking={taken.has(entry.role)}
					disabled={disabled}
					onChoose={onChoose}
					onTake={onTake}
					can={abilities.find((a) => a.role === entry.role)}
					words={roleLabel(title, entry.role)}
				/>
			))}
		</ReviewSection>
	);
}

/** One role: what it can do, the team it goes to, and whether the person joins it. */
function RoleAssignment({
	entry,
	fallback,
	teams,
	taking,
	disabled,
	onChoose,
	onTake,
	can,
	words,
}: {
	entry: RoleTeam;
	fallback: RoleTeam;
	teams: readonly Team[];
	taking: boolean;
	disabled: boolean;
	onChoose: (role: string, teamId: string | undefined) => void;
	onTake: (role: string, take: boolean) => void;
	can: RoleAbilities | undefined;
	/** The role as the target's own ("Example Pages admin"). */
	words: string;
}) {
	const takeId = useId();
	const items: Record<string, string> = { [CONVENTIONAL]: fallback.name };
	for (const team of teams)
		if (team.id !== fallback.team?.id) items[team.id] = team.name;
	const heading = can
		? `What ${article(words).toLowerCase()} ${words} can do`
		: `${words[0]?.toUpperCase() ?? ""}${words.slice(1)}`;
	return (
		<RolePanel role={entry.role} heading={heading}>
			{can && <RoleAbilitiesList abilities={can} label={heading} />}
			<Field
				label="Goes to team"
				description={
					entry.team
						? `existing team ${entry.name}`
						: `${entry.name} will be created`
				}
			>
				{(control) => (
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
							id={control.id}
							aria-describedby={control["aria-describedby"]}
							aria-label={`Team for ${words}`}
							className="w-full sm:w-56"
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
				)}
			</Field>
			{entry.chosen && taking && (
				<p className="text-muted-foreground">
					Joining {entry.name} also gives you whatever else {entry.name} has.
				</p>
			)}
			<div className="flex items-center gap-2">
				<Checkbox
					id={takeId}
					checked={taking}
					disabled={disabled}
					onCheckedChange={(checked) => onTake(entry.role, checked === true)}
				/>
				<Label htmlFor={takeId}>Add me to this team</Label>
			</div>
		</RolePanel>
	);
}
