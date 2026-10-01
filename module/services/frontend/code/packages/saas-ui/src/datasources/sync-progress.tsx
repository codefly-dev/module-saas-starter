"use client";

import { Badge, Button, Card, Progress } from "@codefly-dev/ui/layout";
import { focusManager, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
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

const syncKey = (orgId: string, sourceId: string) =>
	["source-sync", orgId, sourceId] as const;

/**
 * Whether any source of the organization that is being watched right now has a
 * sync in flight, read from the syncs `useSourceSync` already polls — so the
 * panel can poll its lists fast only while there is progress to show, without
 * asking the host anything more.
 *
 * Only watched syncs count: a source removed from the list leaves its last
 * answer in the cache until it is collected, and a sync nobody watches any more
 * must not keep the page polling fast.
 */
export function useAnySourceSyncActive(orgId: string): boolean {
	const cache = useQueryClient().getQueryCache();
	const subscribe = useCallback(
		(onChange: () => void) => cache.subscribe(onChange),
		[cache],
	);
	const read = () =>
		cache.findAll({ queryKey: ["source-sync", orgId] }).some((query) => {
			const sync = query.state.data as SourceSyncView | null | undefined;
			return (
				!!sync && query.getObserversCount() > 0 && describeSync(sync).active
			);
		});
	return useSyncExternalStore(subscribe, read, () => false);
}

/**
 * Now, as state that advances on a tick.
 *
 * Reading `Date.now()` during render would make the component non-idempotent —
 * two renders of the same data disagreeing about what to show — so the clock is
 * a value the component is re-rendered *with*, never one it reads mid-render.
 */
function useNow(enabled: boolean): number {
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => {
		if (!enabled) return;
		const timer = setInterval(() => setNow(Date.now()), CLOCK_TICK_MS);
		return () => clearInterval(timer);
	}, [enabled]);
	return now;
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
		// A sync in flight is followed in a background tab too: without that React
		// Query skips every tick while the page is hidden, and the card froze on
		// the phase it last read ("Queued · Step 1 of 4") until a reload. A settled
		// (or absent) sync is not polled in the background at all — one card per
		// source, each on a 30 s poll in every idle tab, is the load the panel's
		// own idle cadence exists to avoid. Coming back to the tab re-reads it on
		// focus, and the answer restarts the settled cadence.
		refetchInterval: ({ state }) => {
			if (state.data && describeSync(state.data).active) return ACTIVE_POLL_MS;
			return focusManager.isFocused() ? SETTLED_POLL_MS : false;
		},
		refetchIntervalInBackground: true,
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
	const now = useNow(!!sync);
	return {
		...(sync ? { sync, report: describeSync(sync, { now }) } : {}),
		now,
		pending: !!getSourceSync && query.isPending,
		unavailable: !getSourceSync,
	};
}

const toneBadge: Record<
	SyncProgressTone,
	"default" | "secondary" | "destructive" | "outline"
> = {
	info: "secondary",
	success: "outline",
	warning: "default",
	danger: "destructive",
};

const stateLabel: Record<SyncProgressReport["state"], string> = {
	queued: "Queued",
	running: "Running",
	stalling: "No progress",
	retrying: "Retrying",
	done: "Done",
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
	const where = `${Math.min(report.step, report.stepCount)} of ${report.stepCount}`;
	const line =
		report.tone === "danger" ? `Stopped at step ${where}` : `Step ${where}`;
	return report.commit ? `${line} · ${report.commit.slice(0, 7)}` : line;
}

/**
 * One sync's live progress, inside a card.
 *
 * `role="status"` and not `role="alert"`: a sync's phases are polite updates a
 * reader chooses to watch, and announcing each one interrupts whatever they are
 * doing. A failure is announced — that one they need — which is why the
 * container's politeness is raised only for the danger tone.
 */
export function SourceSyncProgress({
	source,
	report,
	onOpenExecution,
	className,
}: {
	source: DatasourceView;
	report: SyncProgressReport;
	/** Offered only when a consumer handed the panel an execution view to open. */
	onOpenExecution?: () => void;
	className?: string;
}) {
	const failed = report.tone === "danger";
	return (
		<Card
			className={cn("space-y-3", className)}
			title={`Sync · ${source.repo}`}
			actions={
				<>
					<Badge variant={toneBadge[report.tone]}>
						{stateLabel[report.state]}
					</Badge>
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
			<div
				role={failed ? "alert" : "status"}
				aria-live={failed ? "assertive" : "polite"}
				className="space-y-2"
			>
				<div className="flex items-baseline justify-between gap-4">
					<span className="type-body">{report.headline}</span>
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
					valueText={report.headline}
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
				{report.detail && (
					<p
						className={cn(
							"type-body",
							failed ? "text-destructive" : "text-muted-foreground",
						)}
					>
						{report.detail}
					</p>
				)}
			</div>
		</Card>
	);
}

/** Exported for the stories and for a consumer rendering its own bar from the same model. */
export { SYNC_STEPS };
