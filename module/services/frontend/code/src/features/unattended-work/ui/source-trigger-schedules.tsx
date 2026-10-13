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
 * "Next" is computed from the declared cron and is what the declaration SAYS.
 * Once a composed module fires the trigger the authoritative cursor is that
 * module's and this host will not hold one; while the stopgap runs, the cron
 * and the stored instant it sweeps on are derived from the same interval, so
 * the two agree without either reading the other.
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
					Each connected source declares its reconcile schedule as a trigger an
					executing module is meant to fire. Until a composed runtime consumes
					that declaration, this host still fires the schedule itself, as a
					stopgap. A person&apos;s &ldquo;Sync now&rdquo; admits the same
					operation with an effect identity of its own, so it is listed beside
					the occurrences rather than apart from them.
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
	const declaration = read.data?.declaration;
	const identity = useMemo(() => declaredIdentity(declaration), [declaration]);
	// The operation is read off the declaration so the manual rows name the
	// same string the scheduled ones do. A literal here would be this page's
	// own opinion of what "the same admission" means, and would keep reading
	// true after the declaration named something else.
	const operation = declaration?.operation ?? RECONCILE_OPERATION;

	// A source that declares no trigger still takes a manual pass: the
	// declaration is refused for the interval it carries, not for the
	// operation, and a person's pass does not go through a schedule at all. So
	// the undeclarable reason replaces the SCHEDULE's rows, never the table.
	return (
		<div className="rounded-lg border p-4" data-slot="source-trigger">
			<div className="flex items-center justify-between gap-2">
				<span className="font-medium">{label}</span>
				{declaration?.undeclarableReason ? (
					<Badge tone="neutral">Declares no trigger</Badge>
				) : (
					<Badge tone="success">{declaration?.schedule?.cron ?? "—"}</Badge>
				)}
			</div>
			<dl className="mt-3 grid gap-2 text-sm sm:grid-cols-3">
				<Fact label="Release" value={declaration?.release ?? "—"} mono />
				<Fact label="Operation" value={operation} mono />
				<Fact label="Declared identity" value={identity} />
			</dl>
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
					{!declaration?.undeclarableReason && (
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
									Scheduled · next occurrence
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
 *
 * The stopgap sweep is a third case and is deliberately claimed as neither: it
 * enqueues under a key derived from the schedule instant and admits no
 * operation at all, so it has no effect identity to show and appears here only
 * as a "last occurrence" moving.
 */
const MINTED_BY_THE_FIRING_MODULE = "Minted by the firing module";

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
 * The reference arm does NOT name which module's delegation it is (issue
 * #1112, m5). The reference value is the source id, so every delegation on one
 * source reads back the same and the host resolves whichever module holds an
 * active one at mint. Naming one here would be this page's own guess: an
 * operator whose executing module holds no delegation would read that a
 * reference is bound to it when another module's is. The references table
 * names each module, which is where that question is answerable.
 */
export function declaredIdentity(declaration?: {
	identity?: { source: string; binding: string };
}): string {
	const identity = declaration?.identity;
	if (identity?.source)
		return "A delegation reference — whichever module holds an active one";
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
