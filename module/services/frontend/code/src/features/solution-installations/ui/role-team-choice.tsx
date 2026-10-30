"use client";

import { SolutionRoleGrantForm } from "@codefly-dev/saas-ui/solution";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { CreateTeamValues } from "@/features/teams/model/schemas";
import { teamMutations } from "@/features/teams/service/mutations";
import { teamQueries } from "@/features/teams/service/queries";
import { TeamForm } from "@/features/teams/ui/team-form";
import type { Team } from "@/gen/saas/accounts/v1/common_pb";
import { Button } from "@/shared/ui";
import { hostRefusal } from "../model/installations";

const sameName = (a: string, b: string) =>
	a.trim().toLowerCase() === b.trim().toLowerCase();

/** The name offered for a new team holding `role` of `solution`; always editable. */
export function suggestedTeamName(solution: string, role: string) {
	return `${solution}-${role}`.trim().toLowerCase().replace(/\s+/g, "-");
}

/**
 * The team for one declared role: an existing organization team, or one created
 * here through the organization's own CreateTeam. A name already taken is
 * surfaced, never reused or merged without the person choosing it.
 */
export function RoleTeamChoice({
	orgId,
	solution,
	role,
	teams,
	disabled,
	onChoose,
	onWorking,
}: {
	orgId: string;
	solution: string;
	role: string;
	teams: Team[];
	disabled: boolean;
	onChoose: (team: Team) => void;
	onWorking: (working: boolean) => void;
}) {
	const client = useQueryClient();
	const [creating, setCreating] = useState(false);
	const [busy, setBusy] = useState(false);
	const [error, setError] = useState<string>();
	const [collision, setCollision] = useState<Team>();
	const close = () => {
		setCreating(false);
		setError(undefined);
		setCollision(undefined);
	};
	const create = async (values: CreateTeamValues) => {
		if (busy) return;
		setBusy(true);
		onWorking(true);
		setError(undefined);
		setCollision(undefined);
		try {
			// Re-read the directory first, so a create whose answer was lost is
			// found rather than repeated.
			const current = await client.fetchQuery({
				...teamQueries.list(orgId),
				staleTime: 0,
			});
			const taken = current.teams.find((team) =>
				sameName(team.name, values.name),
			);
			if (taken) {
				setCollision(taken);
				setError(
					`A team named “${taken.name}” already exists in this organization. Use it explicitly or choose another name.`,
				);
				return;
			}
			const answer = await teamMutations.create(
				orgId,
				values.name.trim(),
				values.description,
			);
			if (!answer.team)
				throw new Error(
					"The host did not return the created team. Try again to look it up.",
				);
			onChoose(answer.team);
			close();
			await client.invalidateQueries({
				queryKey: teamQueries.list(orgId).queryKey,
			});
		} catch (reason) {
			setError(hostRefusal(reason));
		} finally {
			setBusy(false);
			onWorking(false);
		}
	};
	return (
		<div
			className="space-y-2 rounded border p-3"
			aria-label={`Team for ${role}`}
		>
			<h4 className="font-medium">{role}</h4>
			<SolutionRoleGrantForm
				fixedRole={role}
				roles={[role]}
				subjects={teams.map((team) => ({
					kind: "team",
					id: team.id,
					label: team.name,
				}))}
				allowUnlistedSubjects={false}
				subjectLabel="Team"
				submitLabel={`Use team for ${role}`}
				busy={disabled}
				onGrant={({ teamId }) => {
					const team = teams.find((entry) => entry.id === teamId);
					if (team) onChoose(team);
				}}
			/>
			<Button
				type="button"
				variant="outline"
				disabled={disabled}
				onClick={() => {
					close();
					setCreating(true);
				}}
			>
				Create team for {role}
			</Button>
			{creating && (
				<TeamForm
					open
					initial={{ name: suggestedTeamName(solution, role) }}
					isPending={busy}
					error={error}
					onSubmit={(values) => void create(values)}
					onCancel={close}
					existingTeam={
						collision
							? {
									name: collision.name,
									onUse: () => {
										onChoose(collision);
										close();
									},
								}
							: undefined
					}
				/>
			)}
		</div>
	);
}
