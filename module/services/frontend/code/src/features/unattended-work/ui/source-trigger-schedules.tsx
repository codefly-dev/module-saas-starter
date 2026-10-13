"use client";

import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import {
	type AdmittedReconcilePass,
	onSourceReconcileAdmitted,
} from "@codefly-dev/saas-ui";
import { CalendarClock } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { EmptyState } from "@/components/empty-state";
import {
	mayKeepRetainedRows,
	readHasNotAnswered,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Badge,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	DelayedLoading,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
	useLoadingPhase,
} from "@/shared/ui";
import { sourceLabel } from "../model/source-label";
import {
	useSourceTriggerDeclaration,
	useTriggerSources,
} from "../service/queries";

/**
 * A source's reconcile schedule, as the trigger it declares.
 *
 * What a source carries is an interval — its own configuration — and what that
 * interval produces is a declaration an executing module admits occurrences
 * from. So the panel shows the declaration rather than a queue: the cron, the
 * policies, the identity an occurrence borrows, and the last and next
 * occurrence.
 *
 * WHO ACTUALLY FIRES IT TODAY IS THIS HOST, as a labelled stopgap (issue
 * #1112), because nothing consumes the declaration yet. The panel says so
 * rather than describing a schedule nobody runs: before this it showed a
 * "next occurrence" that would never happen, which is the most misleading
 * thing a schedule panel can show.
 *
 * "Next occurrence" is computed from the declared cron and is what the
 * declaration SAYS. Once a composed module fires the trigger the authoritative
 * cursor is that module's and this host will not hold one.
 *
 * WHILE THE STOPGAP RUNS, THAT IS NOT WHEN THE PASS HAPPENS (issue #1121, N6).
 * The two share a period and not a phase: the declared cron for a sub-hourly
 * interval steps by the minute inside the hour, due on the quarter or half
 * hour, while the sweep
 * fires on the source's stored instant, which every pass moves to
 * `now + interval` — an arbitrary offset inside the period. This panel used to
 * claim the two agreed "without either reading the other", which was wrong in
 * the most misleading direction available: a reader was shown 14:30 for a pass
 * that would happen at 14:07. So both are shown, each labelled with who acts
 * on it, and the host's own row disappears when an operator turns the stopgap
 * off.
 *
 * A person's "Sync now" is listed in the SAME table as those occurrences, and
 * that is the panel's claim rather than a convenience: both admit
 * `DatasourceService/ReconcileSource`, both carry an effect identity, and the
 * host's receipts cannot tell them apart. A manual pass that read a prior
 * receipt back is marked as such — it is one admission reported twice, not two.
 *
 * The manual rows are the passes THIS BROWSER admitted, heard from the kit's
 * client, and the panel says so. The host keeps no enumerable list of past
 * passes: a receipt is read back by identity (`LookupReconcileSource`), never
 * listed, so a panel that claimed to show every pass would be inventing one.
 */
export function SourceTriggerSchedules({ orgId }: { orgId: string }) {
	const sources = useTriggerSources(orgId);
	const passes = useAdmittedPasses();
	// The outcome is read BEFORE the row count, because a refused refetch keeps
	// the previous answer: a panel that tested emptiness first would leave rows
	// on screen that the reader is no longer authorized to see.
	const outcome = readOutcome(sources.isError, sources.error);
	const rows = mayKeepRetainedRows(outcome) ? (sources.data ?? []) : [];
	const stale = staleReadNotice(outcome, "this organization's data sources");
	// A disabled query is not a loading one: with no organization selected it
	// sits at isPending with an idle fetch status, and rendering a wait for a
	// read that was never issued is a spinner that never resolves.
	const waiting = sources.isPending && sources.fetchStatus !== "idle";

	return (
		<Card data-slot="source-trigger-schedules">
			<CardHeader>
				<CardTitle>Reconcile schedules</CardTitle>
				<CardDescription>
					Each connected source declares its reconcile schedule as a trigger for
					a module that executes declared work. Whether one consumes that
					declaration is not something this host can confirm, so nothing below
					claims it: what is shown is what this host declares, and what this
					host itself does about it — the stopgap sweep, which fires on the
					source&apos;s own stored instant rather than on the declared cron, so
					those two rows are two different facts. A person&apos;s &ldquo;Sync
					now&rdquo; admits the same operation with an effect identity of its
					own, so it is listed beside the occurrences rather than apart from
					them.
				</CardDescription>
			</CardHeader>
			<CardContent className="space-y-4">
				{stale && (
					<p role="status" className="text-sm text-muted-foreground">
						{stale}
					</p>
				)}
				{waiting ? (
					// The kit's never-flash wait: nothing for the first 200ms, then an
					// indicator that stays long enough to read.
					<DelayedLoading active label="Reading the declared schedules" />
				) : rows.length === 0 ? (
					outcome === "empty" ? (
						<EmptyState
							icon={CalendarClock}
							title="No connected source declares a schedule"
							description="A source declares a reconcile trigger once it is connected and on its schedule."
						/>
					) : (
						<p
							role="status"
							className="py-6 text-center text-sm text-muted-foreground"
						>
							{readOutcomeMessage(
								outcome,
								"this organization's data sources",
								"",
							)}
						</p>
					)
				) : (
					rows.map((source) => (
						<SourceTriggerRow
							key={source.id}
							orgId={orgId}
							sourceId={source.id}
							label={sourceLabel(source)}
							passes={passes.filter((pass) => pass.sourceId === source.id)}
						/>
					))
				)}
			</CardContent>
		</Card>
	);
}

function SourceTriggerRow({
	orgId,
	sourceId,
	label,
	passes,
}: {
	orgId: string;
	sourceId: string;
	label: string;
	/** The manual passes this browser admitted for this source, newest first. */
	passes: AdmittedReconcilePass[];
}) {
	const read = useSourceTriggerDeclaration(orgId, sourceId);
	// THIS ROW'S OWN READ IS CLASSIFIED, not just its data (issue #1121). The
	// source LIST is classified by the panel above; the per-source declaration
	// was not, so a refused read, a read still in flight and a source whose
	// trigger genuinely has no identity all rendered the same sentence — "None
	// — occurrences would be refused", which is a statement about this source's
	// authority that the host never made. A viewer denied the read was told the
	// trigger has no identity rather than that they cannot see it.
	const outcome = readOutcome(read.isError, read.error);
	const declaration = read.data?.declaration;
	const stale = declaration ? staleReadNotice(outcome, "this trigger") : null;
	// AN ANSWER IS STILL COMING, asked once in the read seam rather than
	// re-derived here. `isPending && fetchStatus !== "idle"` was closer than
	// `isFetching` and still missed the state a refused refetch leaves — status
	// `success`, data deleted, retry paused — which is how this row said "no
	// trigger is declared" a third time (issue #1121, r24).
	const unanswered = readHasNotAnswered(read);
	const phase = useLoadingPhase(unanswered);
	const waiting = unanswered || phase.indicator || phase.quiet;
	const identity = useMemo(() => declaredIdentity(declaration), [declaration]);
	// The operation is read off the declaration so the manual rows name the
	// same string the scheduled ones do. A literal here would be this page's
	// own opinion of what "the same admission" means, and would keep reading
	// true after the declaration named something else.
	const operation = declaration?.operation ?? RECONCILE_OPERATION;

	// A source that declares no trigger still takes a manual pass: the
	// declaration is refused for the interval it carries, not for the
	// operation, and a person's pass does not go through a schedule at all. So
	// the undeclarable reason replaces the SCHEDULE's rows, never the table —
	// and neither does a read that did not answer, which is why the manual rows
	// below sit outside every declaration branch.
	return (
		<div className="rounded-lg border p-4" data-slot="source-trigger">
			<div className="flex items-center justify-between gap-2">
				<span className="font-medium">{label}</span>
				{declaration ? (
					declaration.undeclarableReason ? (
						<Badge tone="neutral">Declares no trigger</Badge>
					) : (
						<Badge tone="success">{declaration.schedule?.cron ?? "—"}</Badge>
					)
				) : null}
			</div>
			<DelayedLoading
				active={unanswered}
				label={`Reading the trigger ${label} declares`}
			/>
			{stale && (
				<p role="status" className="mt-2 text-sm text-muted-foreground">
					{stale}
				</p>
			)}
			{/*
			 * The declaration's facts need a declaration. Without one the read
			 * either has not answered or was refused, and the message says
			 * which — never a dash, which would read as "this host has none".
			 */}
			{declaration ? (
				<dl className="mt-3 grid gap-2 text-sm sm:grid-cols-3">
					<Fact label="Release" value={declaration.release || "—"} mono />
					<Fact label="Operation" value={operation} mono />
					<Fact label="Declared identity" value={identity} />
				</dl>
			) : waiting ? null : (
				<p role="status" className="mt-2 text-sm text-muted-foreground">
					{readOutcomeMessage(
						outcome,
						"this source's declared trigger",
						"This source declares no trigger.",
					)}
				</p>
			)}
			{declaration?.undeclarableReason && (
				<p className="mt-2 text-sm text-muted-foreground">
					{declaration.undeclarableReason}
				</p>
			)}
			<Table className="mt-3" data-slot="admissions">
				<TableHeader>
					<TableRow>
						<TableHead>Admission</TableHead>
						<TableHead>Effect identity</TableHead>
						<TableHead>When</TableHead>
						<TableHead>Job</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{/*
					 * THREE SEPARATE CONDITIONS, because they answer three
					 * questions and one flag answered them all wrongly
					 * (issue #1121, the final-amendment review).
					 *
					 * The declaration's own rows need a declaration that
					 * declares something. The host's sweep needs only a
					 * declaration to have ARRIVED — it sweeps a source whose
					 * interval no cron can express just the same. And neither
					 * may render before the read lands: `!declaration?.x` is
					 * true when `declaration` is undefined, so the old guard
					 * rendered the whole block over a read that had failed,
					 * reporting the sweep as "off" about a host nothing had
					 * been established for.
					 */}
					{declaration && !declaration.undeclarableReason && (
						<>
							<TableRow data-slot="scheduled-occurrence">
								<TableCell className="text-sm">
									Scheduled · last occurrence
								</TableCell>
								<TableCell className="text-sm text-muted-foreground">
									{MINTED_BY_THE_FIRING_MODULE}
								</TableCell>
								<TableCell className="text-sm">
									{instant(declaration?.lastOccurrenceAt)}
								</TableCell>
								<TableCell className="text-sm">—</TableCell>
							</TableRow>
							<TableRow data-slot="scheduled-occurrence">
								<TableCell className="text-sm">
									Declared · next occurrence
								</TableCell>
								<TableCell className="text-sm text-muted-foreground">
									{MINTED_BY_THE_FIRING_MODULE}
								</TableCell>
								<TableCell className="text-sm" data-slot="next-occurrence">
									{instant(declaration?.nextOccurrenceAt)}
								</TableCell>
								<TableCell className="text-sm">—</TableCell>
							</TableRow>
						</>
					)}
					{/*
					 * What this host will actually do, for ANY source whose
					 * declaration has arrived — including one that declares no
					 * trigger, which the sweep reconciles on its stored instant
					 * every interval. Hiding it there told an operator this host
					 * does nothing for a source it refreshes on a timer.
					 *
					 * The instant is a different one from the declared
					 * occurrence above, on purpose: see the panel's note.
					 */}
					{declaration && (
						<TableRow data-slot="host-sweep">
							<TableCell className="text-sm">
								{/*
								 * Three states, because the facts supplied are three
								 * (issue #1121). Enabled WITH an instant is a pass
								 * due. Enabled with none is a source the sweep will
								 * not reach — a parked source, whose stored instant
								 * the park path cleared — and "next pass —" read as a
								 * pass whose time was unknown. Disabled is neither.
								 */}
								{!declaration.hostSweepEnabled
									? "This host’s stopgap sweep · off"
									: declaration.hostSweepDueAt
										? "This host’s stopgap sweep · next pass"
										: "This host’s stopgap sweep · on, nothing due for this source"}
							</TableCell>
							<TableCell className="text-sm text-muted-foreground">
								{!declaration.hostSweepEnabled
									? SWEEP_OFF_FIRES_NOTHING_HERE
									: declaration.hostSweepDueAt
										? NO_EFFECT_IDENTITY
										: SWEEP_ON_NOTHING_DUE}
							</TableCell>
							<TableCell className="text-sm" data-slot="host-sweep-due">
								{instant(declaration.hostSweepDueAt)}
							</TableCell>
							<TableCell className="text-sm">—</TableCell>
						</TableRow>
					)}
					{passes.map((pass) => (
						<TableRow key={pass.effectId} data-slot="manual-pass">
							<TableCell className="text-sm">
								{pass.repeat
									? "Manual · receipt read back"
									: "Manual · “Sync now”"}
							</TableCell>
							<TableCell className="font-mono text-xs break-all">
								{pass.effectId}
							</TableCell>
							<TableCell className="text-sm">
								{new Date(pass.at).toLocaleString()}
							</TableCell>
							<TableCell className="font-mono text-xs break-all">
								{pass.jobId || "—"}
							</TableCell>
						</TableRow>
					))}
				</TableBody>
			</Table>
			{passes.length === 0 && (
				<p className="mt-2 text-xs text-muted-foreground">
					No manual pass has been admitted from this browser. A pass is
					recovered by its identity, never listed, so the ones taken elsewhere
					cannot be shown here.
				</p>
			)}
		</div>
	);
}

/**
 * The operation a reconcile admission names, used only when a declaration
 * carries none to read it from — a source that declares no trigger still takes
 * a manual pass of exactly this operation.
 */
const RECONCILE_OPERATION =
	"/saas.accounts.v1.DatasourceService/ReconcileSource";

/**
 * Said rather than left blank: a declared occurrence's effect identity is
 * minted by the module that fires the trigger, so this host never sees one and
 * has nothing to print. A dash would read as "it has none", which would make
 * the admission look unlike the manual pass beside it.
 */
const MINTED_BY_THE_FIRING_MODULE = "Minted by the firing module";

/**
 * The stopgap sweep is a third case and claims neither: it enqueues under a key
 * derived from the schedule instant and admits no operation at all, so it has
 * no effect identity, and its pass shows up in "last occurrence" rather than as
 * an admission of its own.
 */
const NO_EFFECT_IDENTITY = "None — the sweep admits no operation";

/**
 * What a switched-off sweep means, and the one thing it does not mean.
 *
 * It said "An executing module fires the declaration", which asserts that
 * something else does — and nothing this host holds verifies that (issue
 * #1121, F1). The switch says only that THIS host stopped sweeping, so a
 * deployment that turned it off before a module consumed the declaration read
 * as covered while nothing fired at all. The honest statement is the negative
 * one plus the gap.
 */
/**
 * The sweep is running and this source has no instant in it. Said rather than
 * shown as a missing time: the stored instant is what the park and restore
 * paths clear, so its absence is the source being off the schedule — a fact,
 * not a gap in what the host answered.
 */
const SWEEP_ON_NOTHING_DUE =
	"None — this source is not on the sweep's schedule";

const SWEEP_OFF_FIRES_NOTHING_HERE =
	"This host does not fire it — whether an executing module does is unverified here";

/**
 * One labelled fact. Exported because the playground renders the same
 * declaration facts in its own card, and two copies of this would be two
 * places for the declaration to start reading differently.
 */
export function Fact({
	label,
	value,
	mono,
}: {
	label: string;
	value: string;
	mono?: boolean;
}) {
	return (
		<div>
			<dt className="text-xs text-muted-foreground">{label}</dt>
			<dd className={mono ? "font-mono text-xs break-all" : "text-sm"}>
				{value}
			</dd>
		</div>
	);
}

/**
 * The reconcile passes this browser admitted, newest first.
 *
 * A repeat press carries the SAME effect identity as the press it recovers, so
 * it replaces that row rather than adding one: the identity is the pass's name,
 * and two rows under one name would report one admission as two — which is the
 * exact confusion this panel exists to remove.
 */
function useAdmittedPasses(): AdmittedReconcilePass[] {
	const [passes, setPasses] = useState<AdmittedReconcilePass[]>([]);
	useEffect(
		() =>
			onSourceReconcileAdmitted((pass) =>
				setPasses((prior) => [
					pass,
					...prior.filter((held) => held.effectId !== pass.effectId),
				]),
			),
		[],
	);
	return passes;
}

/**
 * Which arm the declaration names, in words. A declaration with neither is the
 * state an installing module refuses rather than running the work as itself,
 * so it is said plainly instead of being left blank.
 *
 * The arm's own value cannot say whose delegation it is — it is the source id,
 * so every delegation on one source reads back the same — so the declaration
 * reports the holder beside it and this names that module.
 *
 * IT DOES NOT CLAIM THE EXECUTOR HOLDS IT (issue #1121, F1). The host sets the
 * arm for a self-addressed delegation, which is the one shape a source-id
 * reference can serve, but nothing it holds declares WHICH module executes the
 * release: those bindings are generic and a deployment may have several. So a
 * source whose only self-addressed delegation belongs to module A reads as a
 * candidate under A's name, and if the composed executor is B — holding none —
 * B's mint is refused and this page must not have said otherwise. Saying "the
 * executing module holds it" was that claim, and it is withdrawn.
 */
export function declaredIdentity(declaration?: {
	identity?: { source: string; binding: string };
	identityHolder?: string;
}): string {
	const identity = declaration?.identity;
	if (identity?.source) {
		const holder = declaration?.identityHolder;
		return holder
			? `A delegation reference held by ${holder} — whether it executes this release is unverified`
			: "A delegation reference — its holder was not reported";
	}
	if (identity?.binding) return "An installation binding";
	return "None — occurrences would be refused";
}

/**
 * One instant, or an em dash. Absent is a real answer here — a source that has
 * never reconciled has no last occurrence — so it is rendered rather than
 * defaulted to a time nothing happened at.
 */
export function instant(value?: Timestamp): string {
	if (!value) return "—";
	return timestampDate(value).toLocaleString();
}
