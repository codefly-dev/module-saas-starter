import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import type { ComponentProps } from "react";
import {
	type CatalogueAuthorization,
	type CatalogueEntry,
	CatalogueEntryKind,
	type CatalogueGap,
	CatalogueGapReason,
	type CatalogueInstallation,
	type CatalogueObservedExecution,
	CatalogueObservedVerdict,
	type CatalogueRelease,
} from "@/gen/saas/accounts/v1/platform_admin_pb";
import { SolutionRegistrationStatus } from "@/gen/saas/accounts/v1/solution_registry_pb";
import type { Badge } from "@/shared/ui";

type StatusTone = NonNullable<ComponentProps<typeof Badge>["tone"]>;

/**
 * One Catalogue fact. A fact the host holds is a `value`; a fact it does not is
 * a `gap` with the server's reason and detail — never an empty cell, a zero, or
 * a match, because each of those is a claim the host cannot make.
 */
export type FactView =
	| { kind: "value"; text: string; tone?: StatusTone; detail?: string }
	| { kind: "gap"; label: string; detail: string };

/** A labelled fact inside one state of the registry's state model. */
export interface StateFact {
	label: string;
	view: FactView;
}

/**
 * The registry's state model, in order. An entry shows each state separately,
 * so desired-but-not-authorized and observed-but-no-longer-authorized read as
 * exactly that rather than collapsing into "declared" and "running".
 */
export const CATALOGUE_STATES = [
	"Desired",
	"Authorized",
	"Applied",
	"Observed",
	"Withdrawing",
	"Retired",
] as const;
export type CatalogueState = (typeof CATALOGUE_STATES)[number];

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

type Oneof = { case: string | undefined; value?: unknown };

// fact reads a value-or-gap oneof: the case ending in "Gap" is the gap, any
// other set case is a value, rendered by format.
function fact<V extends Oneof>(
	oneof: V | undefined,
	format: (value: Exclude<V, { case: undefined }>) => FactView,
): FactView {
	if (!oneof || oneof.case === undefined) return UNSET;
	if (oneof.case.endsWith("Gap")) return gapView(oneof.value as CatalogueGap);
	return format(oneof as Exclude<V, { case: undefined }>);
}

export function releaseText(release: CatalogueRelease): string {
	return `${release.publisher}/${release.name}@${release.version}`;
}

/**
 * An approval names the execution inventory it approved by the digest approval
 * signs, and the member of that inventory this entry is. The inventory itself is
 * the deployment contract's document; the page names it, and does not re-read
 * its fields.
 */
export function approvalText(approval: CatalogueAuthorization): string {
	return `Approved inventory ${approval.inventoryDigest}, member ${approval.memberBinding}, incarnation ${approval.buildIncarnation}.`;
}

export function observationText(
	execution: CatalogueObservedExecution | undefined,
): string {
	if (!execution) return "Nothing observed.";
	const containers = Object.entries(execution.containerImageDigests)
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([container, digest]) => `${container} ${digest}`);
	return `Observed ${containers.join(", ") || "no containers"} at incarnation ${execution.buildIncarnation}.`;
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
	return fact(entry.publisherValue, (v) => ({
		kind: "value",
		text: v.value as string,
	}));
}

/**
 * The self-registration a solution's runtime heartbeats — the only record of a
 * solution's intent this host holds today, so it sits under Desired. A solution
 * named only by installations is desired by nobody this host can see.
 */
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
 * What the platform has approved. "Not authorized" is a statement the approval
 * record makes; a gap is the host being unable to tell — never the same thing.
 */
export function authorizationView(entry: CatalogueEntry): FactView {
	const v = entry.authorized?.authorizationValue;
	if (v?.case === "authorization") {
		return {
			kind: "value",
			text: `Revision ${v.value.authorizedRevision}`,
			tone: "success",
			detail: approvalText(v.value),
		};
	}
	if (v?.case === "notAuthorized") {
		return {
			kind: "value",
			text: "Not authorized",
			tone: "warning",
			detail: v.value.detail,
		};
	}
	return fact(v, () => UNSET);
}

/**
 * What runs, judged against what is authorized. Both failures carry the danger
 * tone: a different execution is what admission refuses, and an execution with
 * no current authorization is what the retirement sweep exists to stop.
 */
export function observedVerdictView(entry: CatalogueEntry): FactView {
	const observed = entry.observed;
	const v = observed?.verdictValue;
	if (v?.case !== "verdict") return fact(v, () => UNSET);
	const running =
		observed?.observedExecutionValue.case === "observedExecution"
			? observed.observedExecutionValue.value
			: undefined;
	const detail = observationText(running);
	switch (v.value) {
		case CatalogueObservedVerdict.RUNNING_AUTHORIZED:
			return {
				kind: "value",
				text: "Running authorized",
				tone: "success",
				detail,
			};
		case CatalogueObservedVerdict.RUNNING_DIFFERS:
			return {
				kind: "value",
				text: "Differs from authorized",
				tone: "danger",
				detail,
			};
		case CatalogueObservedVerdict.RUNNING_UNAUTHORIZED:
			return {
				kind: "value",
				text: "Running without authorization",
				tone: "danger",
				detail,
			};
		default:
			return { ...UNSET, detail };
	}
}

function revisionView(oneof: Oneof | undefined): FactView {
	return fact(oneof, (v) => ({
		kind: "value",
		text: (v.value as bigint).toString(),
	}));
}

/** Each state's facts, labelled as the registry's state model names them. */
export function catalogueStates(
	entry: CatalogueEntry,
): Record<CatalogueState, StateFact[]> {
	return {
		Desired: [
			{ label: "Registration", view: registrationView(entry) },
			{
				label: "Declared release",
				view: fact(entry.desired?.declaredReleaseValue, (v) => ({
					kind: "value",
					text: releaseText(v.value as CatalogueRelease),
				})),
			},
			{
				label: "Declared revision",
				view: revisionView(entry.desired?.declaredRevisionValue),
			},
		],
		Authorized: [{ label: "Approval", view: authorizationView(entry) }],
		Applied: [
			{
				label: "Applied revision",
				view: revisionView(entry.applied?.appliedRevisionValue),
			},
		],
		Observed: [
			{ label: "Running", view: observedVerdictView(entry) },
			{
				label: "Observed revision",
				view: revisionView(entry.observed?.observedRevisionValue),
			},
			{
				label: "Observed at",
				view: fact(entry.observed?.observationFreshnessValue, (v) => ({
					kind: "value",
					text: timestampDate(v.value as Timestamp).toISOString(),
				})),
			},
		],
		Withdrawing: [
			{
				label: "Withdrawal",
				view: fact(entry.withdrawing?.withdrawalStateValue, () => UNSET),
			},
			{
				label: "Credential revocation",
				view: fact(
					entry.withdrawing?.credentialRevocationStateValue,
					() => UNSET,
				),
			},
		],
		Retired: [
			{
				label: "Retirement",
				view: fact(entry.retired?.retirementStateValue, () => UNSET),
			},
		],
	};
}

export function buildSizeView(entry: CatalogueEntry): FactView {
	return fact(entry.buildSizeValue, () => UNSET);
}

export function agentReleaseView(
	installation: CatalogueInstallation,
): FactView {
	return fact(installation.agentReleaseValue, (v) => ({
		kind: "value",
		text: releaseText(v.value as CatalogueRelease),
	}));
}

export function installationRevisionView(
	installation: CatalogueInstallation,
): FactView {
	return fact(installation.revisionValue, () => UNSET);
}

/**
 * The facts this host cannot state, deduplicated, so the page can say once —
 * under the table — what each gap means and what would supply it.
 */
export function catalogueGaps(
	entries: CatalogueEntry[],
): { fact: string; label: string; detail: string }[] {
	const seen = new Map<
		string,
		{ fact: string; label: string; detail: string }
	>();
	const note = (factName: string, view: FactView) => {
		if (view.kind !== "gap") return;
		const key = `${factName}\u0000${view.label}\u0000${view.detail}`;
		if (!seen.has(key))
			seen.set(key, { fact: factName, label: view.label, detail: view.detail });
	};
	for (const entry of entries) {
		note("Publisher", publisherView(entry));
		const states = catalogueStates(entry);
		for (const state of CATALOGUE_STATES) {
			for (const item of states[state]) {
				note(`${state} · ${item.label}`, item.view);
			}
		}
		note("Build size", buildSizeView(entry));
		for (const installation of entry.installations) {
			note("Agent release", agentReleaseView(installation));
			note("Installation revision", installationRevisionView(installation));
		}
	}
	return [...seen.values()];
}
