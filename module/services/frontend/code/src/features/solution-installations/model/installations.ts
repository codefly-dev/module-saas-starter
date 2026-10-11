import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";
import type { ComponentProps } from "react";
import type { Role } from "@/gen/saas/accounts/v1/common_pb";
import {
	type AvailableSolution,
	InstallationHealth,
	type InstallationSummary,
} from "@/gen/saas/accounts/v1/installations_pb";
import type { Badge } from "@/shared/ui";

type StatusTone = NonNullable<ComponentProps<typeof Badge>["tone"]>;

/**
 * One fact on the page. A fact the host stated is a `value`; one it did not is a
 * `gap` with what it means — never an empty cell, because an empty cell reads as
 * an answer.
 */
export type FactView =
	| { kind: "value"; text: string; tone?: StatusTone; detail?: string }
	| { kind: "gap"; label: string; detail: string };

/** The release a target applies, as the Catalogue prints a release. */
export function releaseText(solution: AvailableSolution): string {
	return `${solution.releasePublisher}/${solution.releaseName}@${solution.releaseVersion}`;
}

/**
 * The agent identifier an install creates the solution's agent as. The host
 * documents it as the canonical `publisher/name:version` of the solution's agent,
 * and reads an installation's release back out of exactly that form (the
 * Catalogue's agent release), so the applied release is what an installer is
 * offered. It stays editable: the identifier is the installer's to name, and the
 * host validates it.
 */
export function agentIdentifierFor(solution: AvailableSolution): string {
	return `${solution.releasePublisher}/${solution.releaseName}:${solution.releaseVersion}`;
}

/**
 * Which generation of the target's presence is applied. A target is opened by
 * one generation and later ones may be applied over it; when they differ the
 * reader is told both, because "generation 7" alone hides that the presence is
 * older than the release it runs.
 */
export function generationText(solution: AvailableSolution): string {
	return solution.openedGeneration === solution.appliedGeneration
		? `Generation ${solution.appliedGeneration}`
		: `Generation ${solution.appliedGeneration} (opened at ${solution.openedGeneration})`;
}

export interface AvailableRow {
	targetId: string;
	alias: string;
	release: string;
	generation: string;
	installed: boolean;
	solution: AvailableSolution;
}

/**
 * The catalogue an administrator installs from. Install state is the host's
 * `installed` flag, never inferred from the installation listing: the two reads
 * are separate, and one failing must not make the other say something it did not.
 */
export function availableRows(solutions: AvailableSolution[]): AvailableRow[] {
	return solutions.map((solution) => ({
		targetId: solution.targetId,
		alias: solution.routeAlias,
		release: releaseText(solution),
		generation: generationText(solution),
		installed: solution.installed,
		solution,
	}));
}

export function healthView(health: InstallationHealth): FactView {
	switch (health) {
		case InstallationHealth.HEALTHY:
			return { kind: "value", text: "Healthy", tone: "success" };
		case InstallationHealth.NO_ELIGIBLE_OWNER:
			return {
				kind: "value",
				text: "No eligible owner",
				tone: "danger",
				detail:
					"Neither the owner of record nor any co-owner is an organization administrator any more.",
			};
		case InstallationHealth.AGENT_REVOKED:
			return { kind: "value", text: "Agent revoked", tone: "danger" };
		case InstallationHealth.AGENT_DISABLED:
			return { kind: "value", text: "Agent disabled", tone: "warning" };
		case InstallationHealth.STANDING_GRANT_MISSING:
			return {
				kind: "value",
				text: "Standing grant missing",
				tone: "danger",
				detail:
					"The agent's own grant at the installation's authority root is gone.",
			};
		default:
			return {
				kind: "gap",
				label: "Not reported",
				detail: "The host did not report this installation's health.",
			};
	}
}

export interface InstalledRow {
	installationId: string;
	targetId: string;
	solution: FactView;
	revision: FactView;
	health: FactView;
	installedAt: string | undefined;
}

const NOT_OFFERED: FactView = {
	kind: "gap",
	label: "Not offered",
	detail:
		"This host no longer offers the installed target, so its route and applied release are not known here.",
};

/**
 * The installed list, joined to the catalogue on the immutable target — the
 * join the host prescribes (an installation deliberately carries no route alias;
 * a consumer that needs it asks the catalogue and joins on target_id). A target
 * the catalogue does not list, or an installation that names none, is shown as
 * that gap rather than as a guessed name.
 */
export function installedRows(
	installations: InstallationSummary[],
	available: AvailableSolution[],
	availableKnown: boolean,
): InstalledRow[] {
	const byTarget = new Map(available.map((s) => [s.targetId, s] as const));
	const unknown: FactView = availableKnown
		? NOT_OFFERED
		: {
				kind: "gap",
				label: "Not loaded",
				detail:
					"The available solutions could not be read, so this installation's route and release are not known here.",
			};
	return installations.flatMap(({ installation, health }) => {
		if (!installation) return [];
		const target = byTarget.get(installation.targetId);
		return [
			{
				installationId: installation.id,
				targetId: installation.targetId,
				solution: target ? { kind: "value", text: target.routeAlias } : unknown,
				revision: target
					? {
							kind: "value",
							text: releaseText(target),
							detail: generationText(target),
						}
					: unknown,
				health: healthView(health),
				installedAt: installation.createdAt
					? timestampDate(installation.createdAt).toISOString()
					: undefined,
			},
		];
	});
}

/**
 * Splits a one-per-line (or comma-separated) list into its entries, trimmed and
 * de-duplicated. An empty field is an empty list, which the host reads as "no
 * restriction" — so the page says that beside the field rather than here.
 */
export function parseList(text: string): string[] {
	const seen = new Set<string>();
	for (const entry of text.split(/[\n,]/)) {
		const value = entry.trim();
		if (value) seen.add(value);
	}
	return [...seen];
}

/**
 * A role as an installer chooses it: its name and what it permits, so the
 * least-privilege choice is made from the permissions rather than the name.
 */
export function roleOptionLabel(role: Role): string {
	if (role.permissions.length === 0) return role.name;
	const permits = role.permissions
		.map((p) => `${p.resource}:${p.action}`)
		.join(", ");
	return `${role.name} — ${permits}`;
}

/**
 * The host's own words for why it refused a write. A refusal is shown as the
 * host phrased it — never replaced with a generic "failed", and never with a
 * success the host did not grant.
 */
export function hostRefusal(error: unknown): string {
	const message = ConnectError.from(error).rawMessage.trim();
	return message || "The host refused without giving a reason.";
}

/**
 * Why team exposure cannot be granted from this page. Exposing an installation
 * to a team is a scope grant at its authority-root node (GrantScope / RevokeScope
 * take that node's scope path), and nothing an organization administrator can
 * call returns that path or the team grants already at it: an installation names
 * only the node's id.
 */
export const TEAM_EXPOSURE_UNAVAILABLE =
	"Not available yet: the host does not tell an administrator where this installation's access is granted (its scope path), and granting or revoking a team needs it.";
