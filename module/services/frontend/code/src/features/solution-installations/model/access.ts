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
