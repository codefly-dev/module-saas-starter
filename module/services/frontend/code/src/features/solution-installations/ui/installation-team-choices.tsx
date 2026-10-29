"use client";

import { RoleTeamChoice } from "./role-team-choice";
import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { teamQueries } from "@/features/teams/service/queries";
import type { ReviewedOffer } from "@/gen/host/installation/v1/installation_pb";
import { Button, DelayedLoading } from "@/shared/ui";
import { offerScopeText } from "../model/install-review";
import { hostRefusal } from "../model/installations";
import { useInstallationTeamChoices } from "../service/team-choices";

/** The Setup picker stages explicit choices; it never grants while installing. */
export function InstallationTeamChoices({
	orgId,
	targetId,
	offer,
	disabled = false,
	onApply,
	onBusyChange,
}: {
	orgId: string;
	targetId: string;
	offer: Pick<ReviewedOffer, "approval" | "roles" | "target" | "installation">;
	disabled?: boolean;
	onBusyChange?: (busy: boolean) => void;
	onApply?: (choices: { teamId: string; role: string }[]) => Promise<void>;
}) {
	const teams = useQuery(teamQueries.list(orgId));
	const draft = useInstallationTeamChoices(orgId, targetId);
	const [failure, setFailure] = useState<string>();
	const [busy, setBusy] = useState(false);
	const started = useRef("");
	const [applied, setApplied] = useState(false);
	useEffect(() => { onBusyChange?.(busy); }, [busy, onBusyChange]);
	const choices = draft.value?.choices ?? [];
	const generation = offer.approval?.generation.toString() ?? "";
	const digest = offer.approval?.digest ?? "";
	const changed = Boolean(
		choices.length &&
			(draft.value?.digest !== digest ||
				draft.value?.generation !== generation),
	);
	const teamList = teams.isError ? [] : (teams.data?.teams ?? []);
	const roles = offer.roles.map((role) => role.name);
	const unavailable = choices.some(
		(c) => !roles.includes(c.role) || !teamList.some((t) => t.id === c.teamId),
	);
	const blocked =
		disabled ||
		busy ||
		teams.isPending ||
		teams.isFetching ||
		teams.isError ||
		draft.loading ||
		!digest;
	const save = (next: typeof choices, confirm = false) =>
		draft.save({
			digest: changed && !confirm ? draft.value!.digest : digest,
			generation: changed && !confirm ? draft.value!.generation : generation,
			choices: next,
		});
 const run = useCallback(async () => {
  if (!onApply || !draft.value || blocked || changed || unavailable) return;
  setBusy(true); setFailure(undefined);
  try { await onApply(draft.value.choices); draft.save(null); setApplied(true); }
  catch (error) {
   const reason = hostRefusal(error); setFailure(reason);
   if (draft.value.authorized) draft.save({...draft.value, authorized:{...draft.value.authorized,failure:reason}});
  } finally { setBusy(false); }
 }, [onApply, draft, blocked, changed, unavailable]);
 useEffect(() => {
  const approved = draft.value?.authorized;
  if (!approved?.installationId || approved.failure || !onApply || blocked || approved.installationId !== offer.installation?.id) return;
  const key = `${approved.operationId}:${approved.installationId}`;
  if (started.current === key) return;
  started.current = key;
  if (changed || unavailable) {
   draft.save({...draft.value!, authorized:{...approved, failure:"The selected team or role declaration changed. Review team choices again."}});
  } else { void run(); }
 }, [draft, onApply, blocked, changed, unavailable, offer.installation?.id, run]);
 const pendingAuthorization = Boolean(draft.value?.authorized && !onApply);
 const shownFailure = failure || draft.value?.authorized?.failure;

	return (
		<section aria-label="Team access" className="space-y-3">
			<h3 className="font-medium">Team access</h3>
			<p>
				Choose an existing team or create one for each role this solution declares. Install authorizes these choices; access is applied automatically after setup becomes active. No team members are added. You can change access later in Set up.
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
			{!teams.isPending && !teams.isError && !teamList.length && (
				<p>No teams are available. Create one for a declared role, or assign access later in Set up.</p>
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
								disabled={busy || pendingAuthorization}
								onClick={() => save(choices.filter((x) => x !== c))}
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
			{unavailable && !teams.isPending && (
				<p role="alert">
					A chosen team or role is no longer available. Remove or replace that
					choice.
				</p>
			)}
   {offer.roles.map((role) => <RoleTeamChoice key={role.name} orgId={orgId} solution={offer.target} role={role.name} teams={teamList} disabled={blocked || pendingAuthorization} onWorking={setBusy} onChoose={(team) => save([...choices.filter((choice) => choice.role !== role.name), {teamId:team.id,teamName:team.name,role:role.name}])} />)}
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
			{onApply && choices.length > 0 && (!draft.value?.authorized || shownFailure) && (
				<Button
					type="button"
					disabled={blocked || changed || unavailable}
					onClick={() => void run()}
				>
					{busy ? "Applying team access…" : shownFailure ? "Retry team access" : "Apply team changes"}
				</Button>
			)}
			{pendingAuthorization && <p role="status">Your selected teams are retained. Access will be applied after setup becomes active.</p>}
   {busy && <p role="status">Applying selected team access…</p>}
   {applied && <p role="status">Selected team access is applied.</p>}
   {shownFailure && (
				<p role="alert">
					Team access is incomplete: {shownFailure} Your team choices are retained; retry applying them.
				</p>
			)}
		</section>
	);
}
