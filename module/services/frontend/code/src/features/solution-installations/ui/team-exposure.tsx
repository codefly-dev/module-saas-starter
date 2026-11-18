import { Button, Chip, ChipGroup } from "@/shared/ui";
import type { InstalledRow, TeamGrantRow } from "../model/installations";

/**
 * Team exposure for one installation: the teams granted here, each removable;
 * the teams that reach it from a grant above, which are real reach but were not
 * granted here and so are not removed here; and the action that grants a team.
 */
export function TeamExposure({
	row,
	name,
	granting,
	onGrant,
	onRevoke,
}: {
	row: InstalledRow;
	name: string;
	granting: boolean;
	onGrant: () => void;
	/** Asks before removing: the page confirms, then revokes. */
	onRevoke: (team: TeamGrantRow) => void;
}) {
	return (
		<div className="max-w-xs space-y-2">
			{row.grantedTeams.length === 0 && row.inheritedTeams.length === 0 && (
				<p className="text-sm text-muted-foreground">No team</p>
			)}
			{row.grantedTeams.length > 0 && (
				<ChipGroup label={`Teams granted ${name}`} className="gap-1">
					{row.grantedTeams.map((team) => (
						<Chip
							key={team.key}
							meta={team.role}
							onRemove={() => onRevoke(team)}
							removeLabel={`Remove ${team.team} from ${name}`}
						>
							{team.team}
						</Chip>
					))}
				</ChipGroup>
			)}
			{row.inheritedTeams.length > 0 && (
				<p className="text-xs text-muted-foreground">
					Inherited from above:{" "}
					{row.inheritedTeams.map((team) => team.team).join(", ")}
				</p>
			)}
			{/* A module is used through its roles alone: no second access path. */}
			{row.target?.kind !== "module" && (
				<Button
					variant="outline"
					size="sm"
					aria-label={`Grant a team access to ${name}`}
					disabled={granting}
					onClick={onGrant}
				>
					Grant a team
				</Button>
			)}
		</div>
	);
}
