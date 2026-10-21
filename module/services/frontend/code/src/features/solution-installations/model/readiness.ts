import type {
	InstallationReadiness,
	OrganizationInstallation,
	ReachedModule,
	ReadinessCondition,
} from "@/gen/saas/accounts/v1/installations_pb";
import {
	ReadinessAction,
	ReadinessClearedBy,
	ReadinessConditionKind,
} from "@/gen/saas/accounts/v1/installations_pb";

/**
 * Readiness is what the host said, never recomputed here.
 *
 * The host owns the derivation: it reads the applied manifests, the derived
 * module registry, this organisation's sources and their delegations, and says
 * which conditions are unmet and in whose words. This module turns that into
 * what a reader sees and which control a surface may offer — and nothing else.
 * Deriving readiness in the browser would be a second mechanism for the same
 * decision, and the two would disagree the first time one of them changed.
 */

/** Who can clear a condition, as a reader is told. */
export function clearedByText(by: ReadinessClearedBy): string {
	switch (by) {
		case ReadinessClearedBy.VIEWER:
			return "You";
		case ReadinessClearedBy.ORGANIZATION_ADMIN:
			return "An organization administrator";
		case ReadinessClearedBy.PLATFORM_OPERATOR:
			return "A platform operator";
		case ReadinessClearedBy.NOBODY:
			return "Nobody — the host is waiting";
		default:
			return "The host did not say who can clear this";
	}
}

/**
 * What a surface does about one condition. `perform` is an act this page runs;
 * `elsewhere` names where the act happens when it is not here; `none` is the
 * honest answer for a condition this surface cannot clear at all — an envelope
 * that withholds a binding is a platform operator's, and offering a button that
 * cannot work is the failure this page exists to end.
 */
export type ConditionAffordance =
	| { kind: "perform"; label: string }
	| { kind: "elsewhere"; label: string; href: string }
	| { kind: "none"; label: string };

export function affordance(condition: ReadinessCondition): ConditionAffordance {
	switch (condition.action) {
		case ReadinessAction.RETRY_INSTALL_STEP:
			return { kind: "perform", label: "Retry the install step" };
		case ReadinessAction.RECONNECT_SOURCE:
			return {
				kind: "elsewhere",
				label: "Reconnect this source",
				href: "/admin/datasources",
			};
		case ReadinessAction.INSTALL_REACHED_MODULE:
			return {
				kind: "elsewhere",
				label: `Install ${condition.subject}`,
				href: "/admin/solutions",
			};
		case ReadinessAction.TRANSFER_OWNERSHIP:
			return {
				kind: "none",
				label:
					"Transfer the installation to a current organization administrator. The portal has no surface for that transfer yet; the host's TransferInstallationOwnership is the act.",
			};
		case ReadinessAction.WAIT:
			return {
				kind: "none",
				label: "Nothing brings this forward. Refresh to see the answer land.",
			};
		default:
			return {
				kind: "none",
				label:
					"No action here clears this. The reason above says where it is cleared.",
			};
	}
}

/** The heading one condition is filed under, so like things read together. */
export function conditionHeading(kind: ReadinessConditionKind): string {
	switch (kind) {
		case ReadinessConditionKind.INSTALL_STEP_PENDING:
			return "The install step has not acknowledged";
		case ReadinessConditionKind.INSTALL_STEP_FAILED:
			return "The install step did not succeed";
		case ReadinessConditionKind.RECORD_UNHEALTHY:
			return "The installation record is not healthy";
		case ReadinessConditionKind.MODULE_PRINCIPAL_OMITTED:
			return "The module holds no principal on this host";
		case ReadinessConditionKind.OPERATION_BINDING_OMITTED:
			return "A declared operation binding derived nothing";
		case ReadinessConditionKind.SOURCE_NOT_DELEGATED:
			return "A source holds no delegation to this module";
		case ReadinessConditionKind.REACHED_MODULE_NOT_INSTALLED:
			return "A module this reaches is not installed here";
		case ReadinessConditionKind.CARRIED_MODULE_ACCESS_NOT_DERIVED:
			return "The grant carried no access on a module this reaches";
		default:
			return "The host reported a condition this page does not know";
	}
}

/**
 * What the page says about readiness as a whole. "Ready" is the host's verdict;
 * a count is given beside it because "not ready" with no number sends a reader
 * looking for the list.
 */
export function readinessSummary(
	readiness: InstallationReadiness | undefined,
): {
	ready: boolean;
	text: string;
	tone: "success" | "warning" | "danger";
} {
	if (!readiness) {
		return {
			ready: false,
			text: "Readiness not reported",
			tone: "warning",
		};
	}
	if (readiness.ready) {
		return { ready: true, text: "Ready", tone: "success" };
	}
	const unmet = readiness.unmet.length;
	return {
		ready: false,
		text: `Not ready — ${unmet} unmet ${unmet === 1 ? "condition" : "conditions"}`,
		tone: unmet === 0 ? "warning" : "danger",
	};
}

/** What this installation is, as its registration declared it. */
export function entryKindText(
	row: OrganizationInstallation | undefined,
): string {
	switch (row?.target?.kind) {
		case "module":
			return "Module";
		case "solution":
			return "Solution";
		default:
			return "Module or solution";
	}
}

/** One module a grant has to reach, and whether a team already reaches it. */
export interface ReachOffer {
	audience: string;
	installationId: string;
	label: string;
	/** False when this organisation has not installed the module at all. */
	installed: boolean;
}

/**
 * The modules a grant on this installation also reaches, from the host's own
 * list — the applied manifest's declaration, never derived here.
 *
 * A grant of this installation to a team CARRIES them: the host derives, per
 * module, what the solution declares it consumes there intersected with the role
 * the administrator chose, and grants that at the module's own authority root,
 * revoked with the grant that caused it. It is never a per-module step a person
 * performs — that trail, four bare `Forbidden`s on a validation cell, is what it
 * replaces. The page states the set before anyone grants, so a reader knows what
 * a grant will reach rather than learning it afterwards.
 */
export function reachOffers(reached: ReachedModule[]): ReachOffer[] {
	return reached.map((entry) => ({
		audience: entry.audience,
		installationId: entry.installationId,
		label: entry.routeAlias || entry.audience,
		installed: entry.installationId !== "",
	}));
}
