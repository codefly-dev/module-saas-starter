"use client";

import { Badge, Button, Card, Progress } from "@codefly-dev/ui/layout";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
	describeSync,
	SYNC_STEPS,
	type SyncProgressReport,
	type SyncProgressTone,
} from "./sync-progress-model.js";
import { onSourceSyncRequested } from "./sync-requests.js";
import type {
	DatasourceClient,
	DatasourceView,
	SourceSyncView,
} from "./types.js";
import { cn } from "./util.js";

/** While a sync is in flight; a finished one is re-read only on the panel's own cadence. */
const ACTIVE_POLL_MS = 2_000;
/** Once it is finished there is nothing left to watch, but the source can sync again. */
const SETTLED_POLL_MS = 30_000;
/**
 * How often the clock the report is judged against advances.
 *
 * Load-bearing, not a nicety. A sync that stops advancing produces an identical
 * read on every poll, so nothing about the data changes and the view would never
 * re-render — meaning the state that exists precisely to make silence visible
 * would never appear. The clock is what turns "no new phase" into a render.
 */
const CLOCK_TICK_MS = 15_000;

/**
 * How long a finished sync's card stays up. Its result belongs on the surface
 * that ran it for long enough to be read, and no longer: History is where an
 * older sync lives, and a panel that keeps every outcome becomes a list of
 * cards above the table it is meant to introduce.
 *
 * A failure is not special-cased into permanence. The source's own Status cell
 * carries a source that went degraded, and that one does not age out.
 *
 * It lives here, beside the clock and the poll, because it decides all three:
 * what renders, how long the clock has anything left to change, and when the
 * watch can stop paying for either.
 */
export const SETTLED_SYNC_VISIBLE_MS = 600_000;

const syncKey = (orgId: string, sourceId: string) =>
	["source-sync", orgId, sourceId] as const;

/**
 * Whether a settled sync is recent enough to still be worth showing.
 *
 * An unparseable or absent finish stamp says nothing about how old a settled
 * sync is, so it is treated as old: showing it would pin a card of unknown age
 * above the table for as long as the page is open.
 */
export function withinSettledWindow(
	view: SourceSyncView,
	now: number,
	windowMs = SETTLED_SYNC_VISIBLE_MS,
): boolean {
	const finishedAt = view.finishedAt ? Date.parse(view.finishedAt) : Number.NaN;
	if (Number.isNaN(finishedAt)) return false;
	return now - finishedAt <= windowMs;
}

/**
 * Watches one source's latest sync.
 *
 * Reads the host's phase projection rather than deriving phases from audit
 * history: the projection is stamped from the durable sync and hand-off jobs, so
 * it reports a phase the host can prove and a failure in the host's own words.
 *
 * The poll tightens while a sync is active and slackens once it settles, and
 * `onSourceSyncRequested` re-arms it the moment a sync is enqueued anywhere in
 * the page — so a tenant who presses Sync sees the bar move without waiting out
 * the slow interval. That announcement is the whole reason the client publishes
 * it; before this nothing subscribed.
 *
 * Returns the report as well as the sync, judged against a ticking clock: the
 * caller must not compute it from `Date.now()` itself, or a stalled sync never
 * becomes visible. `now` comes back too, for a caller deciding how long to keep
 * a finished sync on screen.
 */
export function useSourceSync(
	client: DatasourceClient,
	orgId: string,
	source: DatasourceView,
): {
	sync?: SourceSyncView;
	report?: SyncProgressReport;
	now: number;
	/** True while the first read is outstanding; never true for a client that cannot read one. */
	pending: boolean;
	unavailable: boolean;
} {
	const getSourceSync = client.getSourceSync?.bind(client);
	const queryClient = useQueryClient();
	const query = useQuery({
		queryKey: syncKey(orgId, source.id),
		// `null`, not `undefined`: "this source has never synced" is an answer the
		// cache has to be able to hold, and React Query refuses `undefined` as data.
		queryFn: async () => (await getSourceSync?.(orgId, source.id)) ?? null,
		enabled: !!orgId && !!getSourceSync,
		// A read that fails is not a sync that failed, so nothing is rendered for
		// it: the row's own status column already carries the source's health, and
		// inventing a red progress bar out of a transport error would report a
		// healthy sync as broken.
		retry: false,
		// The cadence follows the answer, so nothing has to be kept in step with
		// it. A source with no sync at all polls slowly — it would otherwise poll
		// every two seconds forever, since "no sync yet" never becomes active.
		//
		// The slow poll deliberately survives a sync ageing out of its card: a
		// webhook or a scheduled reconcile starts a sync nobody pressed a button
		// for, and `onSourceSyncRequested` only fires for this page's own Sync.
		// Stopping the poll outright would make every sync this tenant did not
		// personally trigger invisible until a reload. The clock above is the part
		// that can safely stop, because it changes nothing once the card is gone.
		refetchInterval: ({ state }) =>
			state.data && describeSync(state.data).active
				? ACTIVE_POLL_MS
				: SETTLED_POLL_MS,
	});

	// Invalidating on the announcement, rather than waiting for the interval to
	// come round, is what makes the bar appear when the tenant presses Sync: the
	// host may not have stamped the queued phase yet, so this re-asks at once and
	// the interval takes over from the answer.
	useEffect(() => {
		if (!getSourceSync) return;
		return onSourceSyncRequested((syncedId) => {
			if (syncedId !== source.id) return;
			void queryClient.invalidateQueries({
				queryKey: syncKey(orgId, source.id),
			});
		});
	}, [getSourceSync, orgId, source.id, queryClient]);

	const sync = query.data ?? undefined;
	// Now, as state that advances on a tick. Reading `Date.now()` during render
	// would make this non-idempotent — two renders of the same data disagreeing
	// about what to show — so the clock is a value the caller is re-rendered
	// *with*, never one it reads mid-render.
	const [now, setNow] = useState(() => Date.now());
	const report = sync ? describeSync(sync, { now }) : undefined;
	// The clock runs only while moving it can still change something: an
	// in-flight sync (a stall becomes visible *because* `now` advances) or a
	// settled one still inside the window it ages out of. Once it has aged out,
	// a tick changes nothing — the card is already gone — so the interval stops
	// and a watch that renders nothing stops costing a re-render every 15
	// seconds, per source, for the life of the page. It restarts on its own: a
	// later sync arrives on the poll below and makes the report active again.
	const ticking =
		!!sync && !!report && (report.active || withinSettledWindow(sync, now));
	useEffect(() => {
		if (!ticking) return;
		const timer = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
		return () => clearInterval(timer);
	}, [ticking]);
	return {
		...(sync && report ? { sync, report } : {}),
		now,
		pending: !!getSourceSync && query.isPending,
		unavailable: !getSourceSync,
	};
}

/**
 * What each state is called, in the host's own terms.
 *
 * `done` is deliberately NOT "Done". The host reaches that state when every
 * hand-off was *delivered* to the consuming module's queue — it never learns
 * what the module then did with it, so a module that accepts the message and
 * refuses it at its own admission leaves the sync here. "Done" told a tenant
 * their content was ingested on exactly the evidence that it had been posted,
 * and contradicted the headline beside it, which already said "Handed off".
 * Only the module that ingests the files can claim they arrived.
 */
export const syncStateLabel: Record<SyncProgressReport["state"], string> = {
	queued: "Queued",
	running: "Running",
	stalling: "No progress",
	retrying: "Retrying",
	done: "Handed off",
	unchanged: "No changes",
	failed: "Failed",
};

const progressTone: Record<
	SyncProgressTone,
	"neutral" | "success" | "warning" | "danger"
> = {
	info: "neutral",
	success: "success",
	warning: "warning",
	danger: "danger",
};

/**
 * Where the sync is on the ladder, in words — so the phase is legible without
 * colour and without reading a bar.
 *
 * A failed sync's bar is deliberately full: one frozen part-way is
 * indistinguishable from one still moving slowly. That makes the step read as a
 * contradiction unless it says the sync *stopped* there — a full bar over "Step
 * 2 of 4" looks like a rendering fault rather than a sync that got two phases
 * in.
 */
function stepLine(report: SyncProgressReport): string {
	if (report.step <= 0) return "Not started";
	// No clamp: `stepOf` cannot exceed `stepCount` (the lookup tops out at the
	// ladder's length and `done` returns exactly it), so clamping here only
	// suggested to a reader that it could.
	const where = `${report.step} of ${report.stepCount}`;
	const line =
		report.tone === "danger" ? `Stopped at step ${where}` : `Step ${where}`;
	return report.commit ? `${line} · ${report.commit.slice(0, 7)}` : line;
}

/**
 * Whether the source still has an active delegation to deliver through.
 *
 * `"unknown"` is a first-class answer, not a default: the host's own policy
 * makes the delegation listing organization-administrator-only, and a consumer
 * may drive this panel with a client that cannot read it at all. Collapsing
 * that into `"none"` would accuse a perfectly healthy source of having no
 * delegation on the strength of the reader's permissions.
 */
export type SourceDelegationState = "active" | "none" | "unknown";

/**
 * One sync's live progress, inside a card.
 *
 * Two live regions, each scoped to one short line, rather than one region
 * around the whole card. The card used to be a single `status`/`alert` wrapper
 * holding the headline, the bar, the counts and the detail, so every phase
 * change and every per-minute stall update re-announced all of it; and its
 * `role` flipped from `status` to `alert` in the same render that its content
 * became a failure, which is the one case assistive technology is least likely
 * to announce at all — a live region generally has to already exist, with its
 * politeness, before the content inside it changes. So the polite region is now
 * just the status line, and the assertive one is always mounted and empty until
 * there is a failure to put in it.
 */
export function SourceSyncProgress({
	source,
	report,
	delegation = "unknown",
	canManage = true,
	onOpenExecution,
	className,
}: {
	source: DatasourceView;
	report: SyncProgressReport;
	/** Whether the source can still deliver what this sync hands off. */
	delegation?: SourceDelegationState;
	/**
	 * Whether this viewer may act on what the card reports. False adds who can,
	 * because a failure's remedy is an administrator's and the host's own
	 * failure sentences tell the reader to perform it.
	 */
	canManage?: boolean;
	/** Offered only when a consumer handed the panel an execution view to open. */
	onOpenExecution?: () => void;
	className?: string;
}) {
	const failed = report.tone === "danger";
	// The remedy in a host failure sentence ("Reconnect the source with a new
	// token.") is an organization administrator's to perform. Naming who holds
	// it is the same sentence the panel already gives a member for an empty list
	// and for collection access, and without it a member is handed an
	// instruction they have no control for and no explanation.
	const needsAdministrator =
		!canManage && (failed || report.tone === "warning");
	return (
		<Card
			className={cn("space-y-3", className)}
			title={`Sync · ${source.repo}`}
			actions={
				<>
					{/* `tone`, not `variant`: the model's tones are exactly the kit's
					    status tones, and the old variant map painted a warning in the
					    primary brand fill and a success as a bare outline — the one
					    element in this card a reader judges at a glance was the one
					    saying the wrong thing in colour. */}
					<Badge tone={report.tone}>{syncStateLabel[report.state]}</Badge>
					{onOpenExecution && (
						<Button
							type="button"
							variant="outline"
							size="sm"
							onClick={onOpenExecution}
						>
							Execution
						</Button>
					)}
				</>
			}
		>
			<div className="space-y-2">
				<div className="flex items-baseline justify-between gap-4">
					{/* The one polite announcement: the state and where the sync is, as
					    a single line. The state is read because the badge carrying it
					    is not in this region, so a screen reader would otherwise hear
					    the phase and never hear that it failed. */}
					<p role="status" aria-live="polite" className="type-body">
						<span className="sr-only">{syncStateLabel[report.state]}: </span>
						{report.headline}
					</p>
					{report.attempts && (
						<span className="type-body text-muted-foreground">
							{report.attempts}
						</span>
					)}
				</div>
				<Progress
					value={report.percent}
					steps={report.stepCount}
					tone={progressTone[report.tone]}
					label={`Sync progress for ${source.repo}`}
					// The bar's own words say where on the ladder it is. It used to
					// repeat the headline, which the status line above already
					// announces.
					valueText={stepLine(report)}
				/>
				{/* One string rather than interpolated fragments: split across text
				    nodes a screen reader may pause mid-phrase, and "Step", "2" and
				    "of 4" stop being findable as the sentence they read as. */}
				<p className="type-body text-muted-foreground">{stepLine(report)}</p>
				{report.counts.length > 0 && (
					<dl className="flex flex-wrap gap-x-4 gap-y-1 type-body">
						{report.counts.map((count) => (
							<div key={count.label}>
								<dt className="inline text-muted-foreground">
									{count.label}:{" "}
								</dt>
								<dd className="inline">{count.value}</dd>
							</div>
						))}
					</dl>
				)}
				{/* Always mounted so a failure announced into it is announced at all;
				    empty whenever there is nothing wrong. */}
				<div role="alert" aria-live="assertive">
					{failed && report.detail && (
						<p className="type-body text-destructive">{report.detail}</p>
					)}
				</div>
				{!failed && report.detail && (
					<p className="type-body text-muted-foreground">{report.detail}</p>
				)}
				{/* Why a hand-off the host completed can still deliver nothing. The
				    host cannot see the module's own refusal, but it can see that the
				    source has nothing to deliver through, and that is the whole
				    explanation for a sync that reports success and changes nothing. */}
				{delegation === "none" && (
					<p className="type-body text-warning">
						{report.state === "done"
							? "The files were handed off, but this source has no active delegation, so the consuming module cannot accept them."
							: "This source has no active delegation, so nothing it hands off can be accepted."}{" "}
						{canManage
							? "Reconnect the source to delegate it again."
							: "An organization administrator reconnects a source to delegate it again."}
					</p>
				)}
				{needsAdministrator && delegation !== "none" && (
					<p className="type-body text-muted-foreground">
						An organization administrator connects, reconnects and syncs the
						repositories this organization ingests from.
					</p>
				)}
			</div>
		</Card>
	);
}

/** Exported for the stories and for a consumer rendering its own bar from the same model. */
export { SYNC_STEPS };
