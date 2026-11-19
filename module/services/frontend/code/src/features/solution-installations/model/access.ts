import {
	type SetupFact,
	unreachedHeadline,
} from "@codefly-dev/saas-ui/solution";
import type { RoleGrant } from "@/gen/host/installation/v1/installation_pb";
import {
	InstallationHealth,
	InstallationStatus,
} from "@/gen/saas/accounts/v1/installations_pb";
import type { InstalledRow } from "./installations";

/**
 * Where an installation stands, on one line an administrator reads at a
 * glance: "Installed · Ready · Healthy", or what needs them — an update the
 * authority offers, a reach it cannot make now, a condition unmet. Every word
 * is the host's or the authority's answer; nothing is inferred here.
 */
export function statusLine(
	row: InstalledRow,
	unreached: readonly string[] = [],
): SetupFact[] {
	switch (row.status) {
		case InstallationStatus.PENDING:
			return [{ text: "Being set up", tone: "warning" }];
		case InstallationStatus.FAILED:
			return [{ text: "Failed", tone: "danger" }];
		case InstallationStatus.ACTIVE:
			break;
		default:
			return [{ text: "Status not reported", tone: "neutral" }];
	}
	const line: SetupFact[] = [{ text: "Installed", tone: "success" }];
	if (row.readiness?.ready) line.push({ text: "Ready", tone: "success" });
	else if (row.readiness) {
		const unmet = row.readiness.unmet.length;
		line.push({
			text: `${unmet} ${unmet === 1 ? "thing" : "things"} to fix`,
			tone: "danger",
		});
	}
	line.push(healthFact(row.health.kind === "value" ? row.health.text : ""));
	if (row.upgradeAvailable)
		line.push({ text: "Update available", tone: "warning" });
	if (unreached.length)
		line.push({ text: unreachedHeadline(unreached), tone: "warning" });
	return line;
}

function healthFact(text: string): SetupFact {
	return text === "Healthy"
		? { text, tone: "success" }
		: {
				text: text || "Health not reported",
				tone: text ? "danger" : "neutral",
			};
}

/** The health fact the Setup page's status line carries beside the authority's. */
export function healthFacts(health: InstallationHealth): SetupFact[] {
	return health === InstallationHealth.HEALTHY
		? [{ text: "Healthy", tone: "success" }]
		: health === InstallationHealth.UNSPECIFIED
			? []
			: [{ text: "Not healthy", tone: "danger" }];
}

/** One role and the teams and people holding it, by name. */
export interface RoleHolders {
	role: string;
	holders: string[];
}

/**
 * Who holds each role of an installation, from the authority's grants, named
 * as the host names teams and people (an id when it cannot): one entry per
 * role declared, so a role nobody holds says so rather than disappearing.
 */
export function holdersByRole(
	roles: readonly string[],
	grants: readonly RoleGrant[],
	name: (grant: RoleGrant) => string,
): RoleHolders[] {
	const declared = [...new Set([...roles, ...grants.map((g) => g.role)])];
	return declared.sort().map((role) => ({
		role,
		holders: grants
			.filter((g) => g.role === role)
			.map(name)
			.sort(),
	}));
}

/** Who holds which role and the people and teams the host names. */
export interface AccessView {
	grants: Map<string, RoleGrant[]>;
	failed: Set<string>;
	unreached: (installationId: string) => readonly string[];
	roles: (installationId: string) => readonly string[];
	name: (grant: RoleGrant) => string;
}

/**
 * Who an uninstall takes access from, in one phrase ("expenses-editor and
 * expenses-viewer lose": the teams or people holding a role), from the role
 * holders the table already shows. Empty when nobody holds a role or the
 * holders are not read yet.
 */
export function uninstallLosers(
	row: Pick<InstalledRow, "installationId">,
	access: AccessView,
): string {
	const grants = access.grants.get(row.installationId);
	if (!grants) return "";
	const names = grants.map((grant) => ({
		name: access.name(grant),
		id: grant.teamId || grant.personId,
	}));
	// A holder whose name is not read yet would be shown as its id: say the
	// general sentence rather than name anyone by an id.
	if (names.some(({ name, id }) => !name || name === id)) return "";
	const holders = [...new Set(names.map(({ name }) => name))].sort();
	if (holders.length === 0) return "";
	if (holders.length === 1) return `${holders[0]} loses`;
	return `${holders.slice(0, -1).join(", ")} and ${holders[holders.length - 1]} lose`;
}

/** What the page calls an installed row: its target's alias, when the host still offers it. */
export function installedName(row: Pick<InstalledRow, "solution">): string {
	return row.solution.kind === "value"
		? row.solution.text
		: "this installation";
}

/**
 * The question asked before an uninstall: the act and the thing in the title,
 * and who loses access (by name when the holders are read) with what is kept.
 */
export function uninstallQuestion(
	row: Pick<InstalledRow, "installationId" | "solution">,
	access: AccessView,
): { title: string; description: string } {
	const name = installedName(row);
	const losers = uninstallLosers(row, access);
	return {
		title: `Uninstall ${name}?`,
		description: `${
			losers
				? `${losers} access to ${name} at once.`
				: "Everyone who holds one of its roles loses access at once."
		} Its record and audit trail remain. Shared supporting module installations are kept.`,
	};
}
