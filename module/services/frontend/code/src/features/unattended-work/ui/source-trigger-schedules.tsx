"use client";

import { type Timestamp, timestampDate } from "@bufbuild/protobuf/wkt";
import { CalendarClock } from "lucide-react";
import { useMemo } from "react";
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
 * This host fires nothing on this schedule. What a source carries is an
 * interval — its own configuration — and what that interval produces is a
 * declaration an executing module admits occurrences from. So the panel shows
 * the declaration rather than a queue: the cron, the policies, the identity an
 * occurrence borrows, and the last and next occurrence.
 *
 * "Next" is computed from the declared cron and is what the declaration SAYS;
 * the authoritative cursor belongs to the module that fires the trigger, and
 * this host does not hold one.
 */
export function SourceTriggerSchedules({ orgId }: { orgId: string }) {
	const sources = useTriggerSources(orgId);
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
					executing module fires. This host fires nothing on it.
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
}: {
	orgId: string;
	sourceId: string;
	label: string;
}) {
	const read = useSourceTriggerDeclaration(orgId, sourceId);
	const declaration = read.data?.declaration;
	const identity = useMemo(() => declaredIdentity(declaration), [declaration]);

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
			{declaration?.undeclarableReason ? (
				<p className="mt-2 text-sm text-muted-foreground">
					{declaration.undeclarableReason}
				</p>
			) : (
				<Table className="mt-3">
					<TableHeader>
						<TableRow>
							<TableHead>Release</TableHead>
							<TableHead>Identity</TableHead>
							<TableHead>Last occurrence</TableHead>
							<TableHead>Next occurrence</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						<TableRow>
							<TableCell className="font-mono text-xs">
								{declaration?.release ?? "—"}
							</TableCell>
							<TableCell className="text-sm">{identity}</TableCell>
							<TableCell className="text-sm">
								{instant(declaration?.lastOccurrenceAt)}
							</TableCell>
							<TableCell className="text-sm" data-slot="next-occurrence">
								{instant(declaration?.nextOccurrenceAt)}
							</TableCell>
						</TableRow>
					</TableBody>
				</Table>
			)}
		</div>
	);
}

/**
 * Which arm the declaration names, in words. A declaration with neither is the
 * state an installing module refuses rather than running the work as itself,
 * so it is said plainly instead of being left blank.
 */
export function declaredIdentity(declaration?: {
	identity?: { source: string; binding: string };
}): string {
	const identity = declaration?.identity;
	if (identity?.source) return "A delegation reference";
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
