import type { ComponentProps } from "react";
import {
	type CatalogueEntry,
	CatalogueEntryKind,
	type CatalogueExecution,
	type CatalogueGap,
	CatalogueGapReason,
	type CatalogueInstallation,
	type CatalogueRelease,
	CatalogueRunningVerdict,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import { SolutionRegistrationStatus } from "@/gen/saas/accounts/v1/solution_registry_pb";
import type { Badge } from "@/shared/ui";

type StatusTone = NonNullable<ComponentProps<typeof Badge>["tone"]>;

/**
 * One Catalogue cell. A fact the host holds is a `value`; a fact it does not is
 * a `gap` with the server's reason and detail — never an empty cell, a zero, or
 * a match, because each of those is a claim the host cannot make.
 */
export type FactView =
	| { kind: "value"; text: string; tone?: StatusTone; detail?: string }
	| { kind: "gap"; label: string; detail: string };

export function gapLabel(reason: CatalogueGapReason): string {
	switch (reason) {
		case CatalogueGapReason.NOT_RECORDED:
			return "Not recorded";
		case CatalogueGapReason.NOT_REPORTED:
			return "Not reported";
		case CatalogueGapReason.NOT_OBSERVED:
			return "Not observed";
		default:
			return "Unknown";
	}
}

function gapView(gap: CatalogueGap): FactView {
	return { kind: "gap", label: gapLabel(gap.reason), detail: gap.detail };
}

// A oneof the server left unset is not a value either: an older or newer
// server, or a defect, and the cell says it does not know.
const UNSET: FactView = {
	kind: "gap",
	label: "Unknown",
	detail: "The server sent neither a value nor a reason for this fact.",
};

export function releaseText(release: CatalogueRelease): string {
	return `${release.publisher}/${release.name}@${release.version}`;
}

export function executionText(
	execution: CatalogueExecution | undefined,
): string {
	if (!execution) return "unknown";
	return `${execution.imageDigest || "no digest"} (incarnation ${execution.buildIncarnation})`;
}

export function kindLabel(kind: CatalogueEntryKind): string {
	switch (kind) {
		case CatalogueEntryKind.MODULE:
			return "Module";
		case CatalogueEntryKind.SOLUTION:
			return "Solution";
		default:
			return "Unknown";
	}
}

export function publisherView(entry: CatalogueEntry): FactView {
	const v = entry.publisherValue;
	if (v.case === "publisher") return { kind: "value", text: v.value };
	if (v.case === "publisherGap") return gapView(v.value);
	return UNSET;
}

export function declaredReleaseView(entry: CatalogueEntry): FactView {
	const v = entry.declaredReleaseValue;
	if (v.case === "declaredRelease")
		return { kind: "value", text: releaseText(v.value) };
	if (v.case === "declaredReleaseGap") return gapView(v.value);
	return UNSET;
}

export function buildDigestView(entry: CatalogueEntry): FactView {
	const v = entry.buildDigestValue;
	if (v.case === "buildDigest") return { kind: "value", text: v.value };
	if (v.case === "buildDigestGap") return gapView(v.value);
	return UNSET;
}

export function generationView(entry: CatalogueEntry): FactView {
	const v = entry.generationValue;
	if (v.case === "generation")
		return { kind: "value", text: v.value.toString() };
	if (v.case === "generationGap") return gapView(v.value);
	return UNSET;
}

/**
 * What runs beside what was declared. A mismatch is the row an operator is
 * looking for — it is what cluster admission would refuse — so it carries the
 * danger tone and both executions.
 */
export function runningView(entry: CatalogueEntry): FactView {
	const v = entry.runningValue;
	if (v.case === "runningGap") return gapView(v.value);
	if (v.case !== "running") return UNSET;
	const detail = `Declared ${executionText(v.value.declared)}; observed ${executionText(v.value.observed)}.`;
	switch (v.value.verdict) {
		case CatalogueRunningVerdict.MATCHES:
			return {
				kind: "value",
				text: "Matches declared",
				tone: "success",
				detail,
			};
		case CatalogueRunningVerdict.DIFFERS:
			return {
				kind: "value",
				text: "Differs from declared",
				tone: "danger",
				detail,
			};
		default:
			return { ...UNSET, detail };
	}
}

export function buildSizeView(entry: CatalogueEntry): FactView {
	const v = entry.buildSizeValue;
	if (v.case === "buildSizeGap") return gapView(v.value);
	return UNSET;
}

export function agentReleaseView(
	installation: CatalogueInstallation,
): FactView {
	const v = installation.agentReleaseValue;
	if (v.case === "agentRelease")
		return { kind: "value", text: releaseText(v.value) };
	if (v.case === "agentReleaseGap") return gapView(v.value);
	return UNSET;
}

export function installationRevisionView(
	installation: CatalogueInstallation,
): FactView {
	const v = installation.revisionValue;
	if (v.case === "revisionGap") return gapView(v.value);
	return UNSET;
}

export function registrationView(entry: CatalogueEntry): FactView {
	if (entry.kind === CatalogueEntryKind.MODULE) {
		return { kind: "value", text: "Composed", tone: "neutral" };
	}
	const registration = entry.registration;
	if (!registration) {
		return {
			kind: "gap",
			label: "Not registered",
			detail:
				"Named only by its installations; no registration exists under this identifier.",
		};
	}
	switch (registration.status) {
		case SolutionRegistrationStatus.ACTIVE:
			return { kind: "value", text: "Active", tone: "success" };
		case SolutionRegistrationStatus.PENDING:
			return {
				kind: "value",
				text: "Pending",
				tone: "warning",
				detail: "A half has never registered.",
			};
		case SolutionRegistrationStatus.EXPIRED:
			return {
				kind: "value",
				text: "Expired",
				tone: "warning",
				detail: "A half's lease lapsed: its deployment stopped renewing.",
			};
		case SolutionRegistrationStatus.INCOMPATIBLE:
			return {
				kind: "value",
				text: "Incompatible",
				tone: "danger",
				detail: "The two halves declare different contract versions.",
			};
		case SolutionRegistrationStatus.TOMBSTONED:
			return { kind: "value", text: "Deregistered", tone: "neutral" };
		default:
			return UNSET;
	}
}

/**
 * The facts this host cannot state, deduplicated, so the page can say once —
 * under the table — what each "Not recorded" means and what would supply it.
 */
export function catalogueGaps(
	entries: CatalogueEntry[],
): { fact: string; label: string; detail: string }[] {
	const seen = new Map<
		string,
		{ fact: string; label: string; detail: string }
	>();
	const note = (fact: string, view: FactView) => {
		if (view.kind !== "gap") return;
		const key = `${fact}\u0000${view.label}\u0000${view.detail}`;
		if (!seen.has(key))
			seen.set(key, { fact, label: view.label, detail: view.detail });
	};
	for (const entry of entries) {
		note("Publisher", publisherView(entry));
		note("Declared release", declaredReleaseView(entry));
		note("Build", buildDigestView(entry));
		note("Generation", generationView(entry));
		note("Running", runningView(entry));
		note("Build size", buildSizeView(entry));
		for (const installation of entry.installations) {
			note("Agent release", agentReleaseView(installation));
			note("Installation revision", installationRevisionView(installation));
		}
	}
	return [...seen.values()];
}
