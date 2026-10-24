import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ConnectError } from "@connectrpc/connect";
import type { ComponentProps } from "react";
import type { CollectionReadGrant } from "@/gen/saas/accounts/v1/authorization_pb";
import type { Role } from "@/gen/saas/accounts/v1/common_pb";
import {
	InstallationHealth,
	InstallationStatus,
} from "@/gen/saas/accounts/v1/installation_listing_pb";
import type {
	AvailableSolution,
	InstallationReadiness,
	OrganizationInstallation,
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

export function installationStatusView(status: InstallationStatus): FactView {
	switch (status) {
		case InstallationStatus.PENDING:
			return { kind: "value", text: "Pending", tone: "warning" };
		case InstallationStatus.FAILED:
			return { kind: "value", text: "Failed", tone: "danger" };
		case InstallationStatus.ACTIVE:
			return { kind: "value", text: "Active", tone: "success" };
		case InstallationStatus.REVOKED:
			return { kind: "value", text: "Revoked", tone: "danger" };
		default:
			return {
				kind: "gap",
				label: "Not reported",
				detail: "The host did not report this installation's status.",
			};
	}
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

/** One team grant at an installation, as the page lists and revokes it. */
export interface TeamGrantRow {
	key: string;
	teamId: string;
	roleId: string;
	team: string;
	role: string;
	/** Where the grant is written, for the reader who wants to know. */
	scopePath: string;
}

export interface InstalledRow {
	installationId: string;
	targetId: string;
	solution: FactView;
	revision: FactView;
	health: FactView;
	status: InstallationStatus;
	lifecycle: FactView;
	failureReason: string;
	installedAt: string | undefined;
	/** Granted at this installation's authority root: revocable here. */
	grantedTeams: TeamGrantRow[];
	/** Reaching it from a grant above: real reach, but not granted here. */
	inheritedTeams: TeamGrantRow[];
	/**
	 * Whether the host says this installation will WORK, which the health above
	 * does not answer. Carried on the row so the listing can say "needs setup"
	 * and link to the page that lists why; undefined when a host older than the
	 * readiness read served the row.
	 */
	readiness: InstallationReadiness | undefined;
}

const NOT_OFFERED: FactView = {
	kind: "gap",
	label: "Not offered",
	detail:
		"This host no longer offers the installed target, so its route and applied release are not known here.",
};

function teamGrantRows(grants: CollectionReadGrant[]): TeamGrantRow[] {
	return grants.flatMap((view) => {
		const grant = view.grant;
		if (!grant) return [];
		return [
			{
				key:
					grant.id || `${grant.subjectId}:${grant.roleId}:${grant.scopePath}`,
				teamId: grant.subjectId,
				roleId: grant.roleId,
				team: view.subjectLabel || grant.subjectId,
				role: view.roleName || grant.roleId,
				scopePath: grant.scopePath,
			},
		];
	});
}

/**
 * The installed list, from the organization's own listing. The host joins each
 * installation to the catalogue row of the target it installed and says when it
 * no longer offers that target, so nothing here guesses a name.
 */
export function installedRows(
	installations: OrganizationInstallation[],
): InstalledRow[] {
	return installations.flatMap(
		({
			installation,
			health,
			target,
			grantedTeams,
			inheritedTeams,
			readiness,
		}) => {
			if (!installation) return [];
			return [
				{
					installationId: installation.id,
					targetId: installation.targetId,
					solution: target
						? { kind: "value", text: target.routeAlias }
						: NOT_OFFERED,
					revision: target
						? {
								kind: "value",
								text: releaseText(target),
								detail: generationText(target),
							}
						: NOT_OFFERED,
					health: healthView(health),
					status: installation.status,
					lifecycle: installationStatusView(installation.status),
					failureReason: installation.failureReason,
					installedAt: installation.createdAt
						? timestampDate(installation.createdAt).toISOString()
						: undefined,
					grantedTeams: teamGrantRows(grantedTeams),
					inheritedTeams: teamGrantRows(inheritedTeams),
					readiness,
				},
			];
		},
	);
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
