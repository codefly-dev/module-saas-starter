"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import type { ReviewedOffer } from "@/gen/host/installation/v1/installation_pb";
import { Button, DelayedLoading } from "@/shared/ui";
import { offerScopeText } from "../model/install-review";
import { hostRefusal } from "../model/installations";
import {
	choicesMatch,
	type TeamChoice,
	useInstallationTeamChoices,
} from "../service/team-choices";
import { RoleTeamChoice } from "./role-team-choice";

const sameChoice = (a: TeamChoice, b: TeamChoice) =>
	a.teamId === b.teamId && a.role === b.role;

/**
 * One team per role the solution declares. Choosing or creating a team only
 * stages the choice: `grant` is supplied by the Setup surface once the
 * installation is active, so nothing here can confer access before activation.
 */
export function InstallationTeamChoices({
	orgId,
	targetId,
	offer,
	disabled = false,
	onBusyChange,
	grant,
	onApplied,
}: {
	orgId: string;
	targetId: string;
	offer: Pick<ReviewedOffer, "approval" | "roles" | "target" | "installation">;
	disabled?: boolean;
	onBusyChange?: (busy: boolean) => void;
	/** The canonical role grant for one choice; absent until the installation is active. */
	grant?: (choice: TeamChoice) => Promise<void>;
	onApplied?: () => void;
}) {
	const teams = useQuery(teamQueries.list(orgId));
	const draft = useInstallationTeamChoices(orgId, targetId);
	const [creating, setCreating] = useState(false);
	const apply = useMutation({
		mutationFn: async () => {
			if (!grant) return;
			for (const choice of draft.current()?.choices ?? []) {
				await grant(choice);
				// A granted choice leaves the draft, so a retry never repeats it.
				const value = draft.current();
				const rest = value?.choices.filter((c) => !sameChoice(c, choice));
				draft.save(value && rest?.length ? { ...value, choices: rest } : null);
			}
		},
		onError: (error) => {
			const value = draft.current();
			if (value?.authorized)
				draft.save({
					...value,
					authorized: { ...value.authorized, failure: hostRefusal(error) },
				});
		},
		onSettled: () => onApplied?.(),
	});
	const busy = apply.isPending || creating;
	useEffect(() => {
		onBusyChange?.(busy);
	}, [busy, onBusyChange]);

	const choices = draft.value?.choices ?? [];
	const digest = offer.approval?.digest ?? "";
	const changed = !choicesMatch(draft.value, offer.approval);
	const teamList = teams.isError ? [] : (teams.data?.teams ?? []);
	const roles = offer.roles.map((role) => role.name);
	const unavailable =
		teams.isSuccess &&
		choices.some(
			(c) =>
				!roles.includes(c.role) || !teamList.some((t) => t.id === c.teamId),
		);
	const blocked =
		disabled ||
		busy ||
		teams.isPending ||
		teams.isFetching ||
		teams.isError ||
		draft.loading ||
		!digest;
	const save = (next: TeamChoice[], confirm = false) => {
		apply.reset();
		draft.save(
			next.length
				? {
						digest: changed && !confirm ? draft.value!.digest : digest,
						generation:
							changed && !confirm
								? draft.value!.generation
								: (offer.approval?.generation.toString() ?? ""),
						choices: next,
					}
				: null,
		);
	};

	// Choices approved by Install are applied once, when this installation is active.
	const approved = draft.value?.authorized;
	const autoKey =
		approved?.installationId &&
		!approved.failure &&
		approved.installationId === offer.installation?.id
			? `${approved.operationId}:${approved.installationId}`
			: "";
	const started = useRef("");
	useEffect(() => {
		if (!autoKey || !grant || blocked || started.current === autoKey) return;
		started.current = autoKey;
		const value = draft.current();
		if (changed || unavailable) {
			if (value?.authorized)
				draft.save({
					...value,
					authorized: {
						...value.authorized,
						failure:
							"The selected team or the solution's declared roles changed. Review the team choices again.",
					},
				});
		} else apply.mutate();
	}, [autoKey, grant, blocked, changed, unavailable, draft, apply]);

	const failure = apply.error
		? hostRefusal(apply.error)
		: draft.value?.authorized?.failure;
	return (
		<section aria-label="Team access" className="space-y-3">
			<h3 className="font-medium">Team access</h3>
			<p>
				For each role this solution declares, choose an existing team or create
				one. No team members are added. Access is granted only once setup is
				active, and you can change it later in Set up.
			</p>
			<details>
				<summary>What each declared role permits</summary>
				<ul>
					{offer.roles.map((role) => (
						<li key={role.name}>
							{role.name}:{" "}
							{role.scopes.map(offerScopeText).join("; ") ||
								"No data access declared."}
						</li>
					))}
				</ul>
			</details>
			<DelayedLoading
				active={teams.isFetching || draft.loading}
				label="Loading teams"
			/>
			{teams.isError && (
				<p role="alert">Teams could not be read: {hostRefusal(teams.error)}</p>
			)}
			{teams.isSuccess && !teamList.length && (
				<p>
					No teams exist yet. Create one for a declared role, or assign access
					later in Set up.
				</p>
			)}
			{choices.length > 0 && (
				<ul aria-label="Chosen team roles" className="space-y-1">
					{choices.map((c) => (
						<li key={`${c.teamId}:${c.role}`}>
							{c.teamName} — {c.role}{" "}
							<Button
								type="button"
								size="sm"
								variant="ghost"
								disabled={disabled || busy}
								onClick={() => save(choices.filter((x) => !sameChoice(x, c)))}
							>
								Remove {c.teamName} {c.role}
							</Button>
						</li>
					))}
				</ul>
			)}
			{changed && (
				<p role="alert">
					The solution declaration changed. Review its current roles and team
					choices before applying access.
				</p>
			)}
			{unavailable && (
				<p role="alert">
					A chosen team or role is no longer available. Remove or replace that
					choice.
				</p>
			)}
			{offer.roles.map((role) => (
				<RoleTeamChoice
					key={role.name}
					orgId={orgId}
					solution={offer.target}
					role={role.name}
					teams={teamList}
					disabled={blocked}
					onWorking={setCreating}
					onChoose={(team) =>
						save([
							...choices.filter((c) => c.role !== role.name),
							{ teamId: team.id, teamName: team.name, role: role.name },
						])
					}
				/>
			))}
			{changed && (
				<Button
					type="button"
					variant="outline"
					disabled={blocked || unavailable}
					onClick={() => save(choices, true)}
				>
					Confirm team choices for current declaration
				</Button>
			)}
			{grant && choices.length > 0 && (!approved || failure) && (
				<Button
					type="button"
					disabled={blocked || changed || unavailable}
					onClick={() => apply.mutate()}
				>
					Apply team access
				</Button>
			)}
			{!grant && choices.length > 0 && (
				<p role="status">
					Your team choices are kept. They are granted only after setup is
					active.
				</p>
			)}
			{apply.isPending && <p role="status">Applying team access…</p>}
			{apply.isSuccess && !choices.length && (
				<p role="status">Team access is applied.</p>
			)}
			{failure && (
				<p role="alert">
					Team access is incomplete: {failure} Your team choices are retained;
					retry applying them.
				</p>
			)}
		</section>
	);
}
