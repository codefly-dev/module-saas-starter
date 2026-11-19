"use client";

import { useState } from "react";
import { toast } from "sonner";
import { failureText } from "@/shared/lib/failure-text";
import { installedName } from "../model/access";
import {
	hostRefusal,
	type InstalledRow,
	type TeamGrantRow,
} from "../model/installations";
import { retryOutcome } from "../model/outcomes";
import type { GrantTeamValues } from "../model/schemas";
import {
	useGrantInstallationToTeam,
	useRetryInstallation,
	useRevokeInstallationFromTeam,
	useUninstallSolution,
} from "../service/mutations";

/** A team's grant at an installation, chosen for removal. */
export interface TeamRemoval {
	row: InstalledRow;
	team: TeamGrantRow;
}

/**
 * The acts an administrator performs on an installed row — retry, uninstall,
 * grant a team, remove a team — and what the page keeps about them: which
 * question is open, and the last refusal. Each act is the host's to accept or
 * refuse; a refusal stays on the page in the host's words, never a toast that
 * is gone before it is read, and never a success the host did not grant.
 */
export function useInstalledActs(orgId: string) {
	const uninstall = useUninstallSolution(orgId);
	const retry = useRetryInstallation(orgId);
	const revoke = useRevokeInstallationFromTeam(orgId);
	const grant = useGrantInstallationToTeam(orgId);
	const [uninstalling, setUninstalling] = useState<InstalledRow | null>(null);
	const [removing, setRemoving] = useState<TeamRemoval | null>(null);
	const [granting, setGranting] = useState<InstalledRow | null>(null);
	const [refusal, setRefusal] = useState<{
		title: string;
		reason: string;
	} | null>(null);

	const refused = (act: string) => (error: unknown) =>
		setRefusal({ title: `Couldn't ${act}`, reason: hostRefusal(error) });

	return {
		refusal,
		dismissRefusal: () => setRefusal(null),
		uninstalling,
		removing,
		granting,
		busy: {
			retrying: retry.isPending,
			uninstalling: uninstall.isPending,
			revoking: revoke.isPending,
			granting: grant.isPending,
		},
		grantError: grant.isError ? hostRefusal(grant.error) : undefined,

		retry: (row: InstalledRow) => {
			const name = installedName(row);
			setRefusal(null);
			retry.mutate(row.installationId, {
				onSuccess: (installation) => {
					const outcome = retryOutcome(name, installation);
					toast[outcome.kind](outcome.text);
				},
				onError: refused(`retry ${name}`),
			});
		},

		askUninstall: (row: InstalledRow) => setUninstalling(row),
		cancelUninstall: () => setUninstalling(null),
		confirmUninstall: () => {
			if (!uninstalling) return;
			const name = installedName(uninstalling);
			setRefusal(null);
			uninstall.mutate(uninstalling.installationId, {
				onSuccess: () => toast.success(`${name} uninstalled`),
				onError: refused(`uninstall ${name}`),
				// The question stays open while the host decides, and closes on
				// its answer: a refusal is then read on the page beneath it.
				onSettled: () => setUninstalling(null),
			});
		},

		askRemove: (row: InstalledRow, team: TeamGrantRow) =>
			setRemoving({ row, team }),
		cancelRemove: () => setRemoving(null),
		confirmRemove: () => {
			if (!removing) return;
			const { row, team } = removing;
			const name = installedName(row);
			setRefusal(null);
			revoke.mutate(
				{
					installationId: row.installationId,
					teamId: team.teamId,
					roleId: team.roleId,
				},
				{
					onSuccess: () =>
						toast.success(
							`${team.team}'s ${team.role} grant removed from ${name}`,
						),
					onError: refused(`remove ${team.team} from ${name}`),
					onSettled: () => setRemoving(null),
				},
			);
		},

		openGrant: (row: InstalledRow) => {
			grant.reset();
			setGranting(row);
		},
		closeGrant: () => setGranting(null),
		submitGrant: (values: GrantTeamValues) => {
			if (!granting) return;
			const name = installedName(granting);
			grant.mutate(
				{ installationId: granting.installationId, ...values },
				{
					onSuccess: () => {
						toast.success(`Team grant recorded for ${name}`);
						setGranting(null);
					},
					// The dialog may have been dismissed while the host decided:
					// the refusal still reaches the person.
					onError: (error) =>
						toast.error(failureText(`grant the team access to ${name}`, error)),
				},
			);
		},
	};
}
