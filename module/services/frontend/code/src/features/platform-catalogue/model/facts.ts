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
 * The registry's state model, in order, limited to the states this platform can
 * report a value for. An entry shows each separately, so
 * desired-but-not-authorized and observed-but-no-longer-authorized read as
 * exactly that rather than collapsing into "declared" and "running".
 *
 * Withdrawing and Retired are absent on purpose: their messages in
 * `platform_admin.proto` are gap-only oneofs, so a column for either could
 * never hold anything but "Not recorded". See UNBUILT_FACTS.
 */
export const CATALOGUE_STATES = [
	"Desired",
	"Authorized",
	"Applied",
	"Observed",
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
				detail: "Its applied presence declares no frontend block.",
			};
		// No EXPIRED and no INCOMPATIBLE case: this host has no lease to lapse
		// and nothing a running process reports to compare. Status is derived
		// from delivered presence alone — the enum dropped both values with the
		// lease and the reported halves. A record whose removal applied is
		// TOMBSTONED, which is not an expiry.
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
 * Why an affirmative verdict may not be shown, or undefined when it may.
 *
 * "Running authorized" is the one cell a reader acts on by doing nothing, so the
 * page shows it only beside the evidence it rests on: an approval complete enough
 * to judge against, an observed execution, and the time that observation was
 * made. The server applies the same rule where the verdict is made and again at
 * its read boundary, so a response that arrives contradicting itself is a defect
 * — and a defect must read as a fact this host cannot state, never as green.
 *
 * It is the presence of each piece of evidence that is checked here, never its
 * age: how long an observation speaks for the present is the host's to decide,
 * and a second copy of that window in a browser would drift from it. The
 * observation's own time is shown beside the verdict, so an operator reads the
 * age for themselves.
 *
 * The cell reads "Inconsistent" rather than borrowing one of the server's gap
 * reasons: the server did not say it could not tell, it said something its own
 * response contradicts, and conflating the two would hide a defect among the
 * facts this host simply does not hold yet.
 */
function affirmativeVerdictUnsupported(
	entry: CatalogueEntry,
): string | undefined {
	const unsupported = (missing: string) =>
		`The server reported the approved execution running, ${missing}. Without that evidence this host cannot say what runs, so it is not shown as a match.`;
	const authorization = entry.authorized?.authorizationValue;
	if (authorization?.case !== "authorization")
		return unsupported("with no approval beside it to be authorized against");
	if (authorization.value.buildIncarnation === BigInt(0))
		return unsupported("against an approval that carries no build incarnation");
	const observed = entry.observed;
	if (observed?.observedExecutionValue.case !== "observedExecution")
		return unsupported("with no observed execution beside it");
	if (observed.observationFreshnessValue.case !== "observedAt")
		return unsupported("without the time the observation was made");
	return undefined;
}

/**
 * What runs, judged against what is authorized. Both failures carry the danger
 * tone: a different execution is what admission refuses, and an execution with
 * no current authorization is what the retirement sweep exists to stop. Neither
 * is withheld for want of the evidence an affirmative verdict needs: a partly
 * evidenced report of trouble is still a report of trouble.
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
		case CatalogueObservedVerdict.RUNNING_AUTHORIZED: {
			const unsupported = affirmativeVerdictUnsupported(entry);
			if (unsupported)
				return { kind: "gap", label: "Inconsistent", detail: unsupported };
			return {
				kind: "value",
				text: "Running authorized",
				tone: "success",
				detail,
			};
		}
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
	};
}

export function agentReleaseView(
	installation: CatalogueInstallation,
): FactView {
	return fact(installation.agentReleaseValue, (v) => ({
		kind: "value",
		text: releaseText(v.value as CatalogueRelease),
	}));
}

export interface CatalogueNote {
	fact: string;
	label: string;
	detail: string;
}

function collector() {
	const seen = new Map<string, CatalogueNote>();
	return {
		note(fact: string, view: FactView) {
			if (view.kind !== "gap") return;
			const key = `${fact}\u0000${view.label}\u0000${view.detail}`;
			if (!seen.has(key))
				seen.set(key, { fact, label: view.label, detail: view.detail });
		},
		notes: () => [...seen.values()],
	};
}

/**
 * The facts this host could hold and does not, deduplicated, so the page can
 * say once — under the table — what each unavailable cell means and what would
 * supply it.
 */
export function catalogueGaps(entries: CatalogueEntry[]): CatalogueNote[] {
	const { note, notes } = collector();
	for (const entry of entries) {
		note("Publisher", publisherView(entry));
		const states = catalogueStates(entry);
		for (const state of CATALOGUE_STATES) {
			for (const item of states[state]) {
				note(`${state} · ${item.label}`, item.view);
			}
		}
		for (const installation of entry.installations) {
			note("Agent release", agentReleaseView(installation));
		}
	}
	return notes();
}

/**
 * The facts whose contract carries no value case at all.
 *
 * `CatalogueWithdrawing`, `CatalogueRetired`, an entry's build size and an
 * installation's own revision are gap-only oneofs in `platform_admin.proto`:
 * the records that would fill them — a withdrawal and revocation record, a
 * retirement controller reporting to this host, the presence document's
 * build_size section — have not been built. A column for any of them could
 * never hold anything but its gap.
 *
 * So they are not columns. A grid of "Not recorded" reads as a deployment an
 * operator failed to configure, when it is machinery nobody has written yet;
 * the two are different facts and the page states them separately. Each one's
 * reason is still the server's own, read from the gap it sends.
 */
export function unbuiltFacts(entries: CatalogueEntry[]): CatalogueNote[] {
	const { note, notes } = collector();
	for (const entry of entries) {
		note(
			"Withdrawal",
			fact(entry.withdrawing?.withdrawalStateValue, () => UNSET),
		);
		note(
			"Credential revocation",
			fact(entry.withdrawing?.credentialRevocationStateValue, () => UNSET),
		);
		note(
			"Retirement",
			fact(entry.retired?.retirementStateValue, () => UNSET),
		);
		note(
			"Build size",
			fact(entry.buildSizeValue, () => UNSET),
		);
		for (const installation of entry.installations) {
			note(
				"Installation revision",
				fact(installation.revisionValue, () => UNSET),
			);
		}
	}
	return notes();
}
