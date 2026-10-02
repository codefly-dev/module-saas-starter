/**
 * What to show for one sync of a source, derived from the host's own phase
 * projection (`SourceSyncView`) and nothing else.
 *
 * The projection is stamped from durable records — the sync job and the
 * change-set jobs it handed off — so every phase here is a fact the host can
 * prove, not a guess from elapsed time. This module only decides how to say it.
 *
 * Time enters in exactly one place, and it is presentation rather than
 * inference: a sync that has not reached its next phase for a long while is
 * reported as `stalling`, which says "no progress since X" and never "failed".
 * The host is the only thing that may call a sync failed, and it does, through
 * `failure`.
 */

import type { SourceSyncView } from "./types.js";

/** The step ladder a sync climbs. `done` is terminal, so it is not a step. */
export const SYNC_STEPS = [
	"queued",
	"fetching",
	"compiled",
	"handed_off",
] as const;

export type SyncStepName = (typeof SYNC_STEPS)[number];

/**
 * How a sync reads right now.
 *
 * - `queued` / `running` — in flight, the bar advances.
 * - `stalling` — in flight, but the current phase has not moved for a while.
 *   Still running: the host has neither failed it nor given up on it.
 * - `retrying` — the host failed an attempt and will try again. It carries the
 *   reason, because a tenant can often act on it (a bad credential) before the
 *   retry lands.
 * - `done` — finished, with a change set.
 * - `unchanged` — finished, having handed nothing off: the source had not
 *   moved. Distinct from `done` because "0 files" reads as a failure otherwise.
 * - `failed` — the host stopped retrying.
 */
export type SyncProgressState =
	| "queued"
	| "running"
	| "stalling"
	| "retrying"
	| "done"
	| "unchanged"
	| "failed";

/** Whether the state is one the reader should be alarmed by. */
export type SyncProgressTone = "info" | "success" | "warning" | "danger";

export interface SyncProgressCount {
	label: string;
	value: number;
}

export interface SyncProgressReport {
	state: SyncProgressState;
	tone: SyncProgressTone;
	/** True while the host may still advance this sync; drives whether to keep polling. */
	active: boolean;
	/** One line naming where the sync is. */
	headline: string;
	/** The phase ladder, for a stepped bar. `step` is 1-based; 0 before `queued`. */
	step: number;
	stepCount: number;
	/**
	 * The bar's fill, 0–100. A terminal state is always 100 — including a failed
	 * one, whose bar is filled and red rather than stuck part-way, so a reader
	 * cannot mistake "stopped" for "still going".
	 */
	percent: number;
	/** The change set, once compiled. Empty until then. */
	counts: SyncProgressCount[];
	/**
	 * The one thing the reader most needs after the headline: a failure's
	 * host-authored sentence, how long a stall has lasted, when a retry lands.
	 * Absent when the headline says everything.
	 */
	detail?: string;
	/** `attempt of maxAttempts`, only while that is worth saying. */
	attempts?: string;
	/** The commit the change set brings the source to, once known. */
	commit?: string;
}

/**
 * How long a phase may go without advancing before the view says so.
 *
 * This is the one number in the datasource views that is a judgement rather
 * than a fact, so it is named and exported: a consumer whose repositories are
 * large enough that two minutes of fetching is normal raises it rather than
 * teaching its users to ignore a warning.
 */
export const DEFAULT_STALL_AFTER_MS = 120_000;

const STEP_OF: Record<SyncStepName, number> = {
	queued: 1,
	fetching: 2,
	compiled: 3,
	handed_off: 4,
};

/** The phase the ladder is on, or undefined for a phase that is not a step. */
function stepOf(view: SourceSyncView): number {
	const step = STEP_OF[view.phase as SyncStepName];
	if (step) return step;
	// `done` sits past the ladder; `failed` and `unknown` are placed by the
	// furthest timestamp the host stamped, so a failed sync's bar still shows
	// how far it had got.
	if (view.phase === "done") return SYNC_STEPS.length;
	if (view.handedOffAt) return 4;
	if (view.compiledAt) return 3;
	if (view.fetchingAt) return 2;
	if (view.queuedAt) return 1;
	return 0;
}

const STEP_HEADLINE: Record<SyncStepName, string> = {
	// Not "Queued": that is the state's name, which a view shows beside this. The
	// headline's job is to add what the state does not say — here, that the sync
	// is accepted and waiting on a worker rather than being looked at.
	queued: "Accepted, waiting for a worker",
	fetching: "Fetching from the repository",
	compiled: "Compiling the change set",
	handed_off: "Handed off for ingestion",
};

/**
 * When the current phase was entered — the clock a stall is measured against.
 * The latest stamp the host wrote, never `Date.now()` at first render, so a
 * remount does not reset a stall that has been going for ten minutes.
 */
function enteredCurrentPhaseAt(view: SourceSyncView): number | undefined {
	for (const iso of [
		view.handedOffAt,
		view.compiledAt,
		view.fetchingAt,
		view.queuedAt,
	]) {
		const at = iso ? Date.parse(iso) : Number.NaN;
		if (!Number.isNaN(at)) return at;
	}
	return undefined;
}

function countsOf(view: SourceSyncView): SyncProgressCount[] {
	const changes = view.changes;
	if (!changes) return [];
	const counts: SyncProgressCount[] = [
		{ label: "Files", value: changes.files },
	];
	// A snapshot after a force push knows its file count but not the split, and
	// rendering three zeros beside a real total would read as "nothing changed".
	if (changes.splitKnown) {
		counts.push(
			{ label: "Added", value: changes.added },
			{ label: "Modified", value: changes.modified },
			{ label: "Deleted", value: changes.deleted },
		);
	}
	return counts;
}

function formatDuration(ms: number): string {
	const minutes = Math.floor(ms / 60_000);
	if (minutes < 1) return "less than a minute";
	if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"}`;
	const hours = Math.floor(minutes / 60);
	return `${hours} hour${hours === 1 ? "" : "s"}`;
}

export interface DescribeSyncOptions {
	/** Now, in epoch ms. Injected so every state is testable without a fake clock. */
	now?: number;
	/** Overrides `DEFAULT_STALL_AFTER_MS`. */
	stallAfterMs?: number;
}

/**
 * Turns one sync into what to render. Pure: same view and same clock, same
 * report.
 */
export function describeSync(
	view: SourceSyncView,
	options: DescribeSyncOptions = {},
): SyncProgressReport {
	const now = options.now ?? Date.now();
	const stallAfterMs = options.stallAfterMs ?? DEFAULT_STALL_AFTER_MS;
	const step = stepOf(view);
	const stepCount = SYNC_STEPS.length;
	const counts = countsOf(view);
	const commit = view.changes?.commit || undefined;
	const attempts =
		view.maxAttempts > 1 && view.attempt > 1
			? `Attempt ${view.attempt} of ${view.maxAttempts}`
			: undefined;

	const base = {
		step,
		stepCount,
		counts,
		...(commit ? { commit } : {}),
		...(attempts ? { attempts } : {}),
	};

	// Failed for good. The host says so; nothing here second-guesses it.
	if (view.phase === "failed" || (view.failure && !view.failure.retrying)) {
		return {
			...base,
			state: "failed",
			tone: "danger",
			active: false,
			percent: 100,
			headline: "Sync failed",
			...(view.failure?.message ? { detail: view.failure.message } : {}),
		};
	}

	// Still going, but the last attempt failed. Said before the phase, because
	// the reason is usually the actionable part and the phase is not.
	if (view.failure?.retrying) {
		const retryAt = view.failure.retryAt
			? Date.parse(view.failure.retryAt)
			: Number.NaN;
		const waitFor =
			!Number.isNaN(retryAt) && retryAt > now
				? ` Next attempt in ${formatDuration(retryAt - now)}.`
				: "";
		return {
			...base,
			state: "retrying",
			tone: "warning",
			active: true,
			percent: percentFor(step, stepCount),
			headline: "Retrying after a failed attempt",
			detail: `${view.failure.message}${waitFor}`.trim(),
		};
	}

	if (view.phase === "done") {
		// Compiled nothing: the source had not moved. The host leaves `changes`
		// unset for exactly this, and it is a success, not an empty failure.
		if (!view.changes) {
			return {
				...base,
				state: "unchanged",
				tone: "success",
				active: false,
				percent: 100,
				headline: "Up to date",
				detail: "The source had not changed, so nothing was handed off.",
			};
		}
		return {
			...base,
			state: "done",
			tone: "success",
			active: false,
			percent: 100,
			headline: view.changes.snapshot
				? "Handed off a full snapshot"
				: "Handed off the changed files",
		};
	}

	// In flight. A phase that has not advanced for a long time is reported as
	// such — with what it is waiting on and for how long — rather than left as a
	// bar that looks alive.
	const enteredAt = enteredCurrentPhaseAt(view);
	const waiting = enteredAt === undefined ? 0 : now - enteredAt;
	const headline =
		STEP_HEADLINE[view.phase as SyncStepName] ?? "Sync in progress";
	if (waiting >= stallAfterMs) {
		return {
			...base,
			state: "stalling",
			tone: "warning",
			active: true,
			percent: percentFor(step, stepCount),
			headline,
			detail: `No progress for ${formatDuration(waiting)}. The host has not given up; open the execution view for the durable attempts.`,
		};
	}
	return {
		...base,
		state: view.phase === "queued" ? "queued" : "running",
		tone: "info",
		active: true,
		percent: percentFor(step, stepCount),
		headline,
	};
}

/**
 * The bar's fill for an in-flight sync. A step is shown as *entered*, not
 * completed — `queued` fills a quarter — so a bar at 0 always means "the host
 * has not accepted this yet" rather than "queued".
 */
function percentFor(step: number, stepCount: number): number {
	if (step <= 0) return 0;
	return Math.round((step / stepCount) * 100);
}
