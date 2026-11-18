"use client";

import { Database, Play } from "lucide-react";
import Link from "next/link";
import { useState } from "react";
import { toast } from "sonner";
import { OrgSelector } from "@/components/org-selector";
import type {
	TriggerChildContext,
	TriggerDelegationReference,
} from "@/gen/saas/accounts/v1/datasource_pb";
import { useAuth } from "@/lib/auth";
import { failureText } from "@/shared/lib/failure-text";
import {
	mayKeepRetainedRows,
	type ReadOutcome,
	readOutcome,
	staleReadNotice,
} from "@/shared/lib/read-outcome";
import {
	Banner,
	Button,
	DelayedLoading,
	EmptyState,
	ErrorState,
	Field,
	Page as PageBody,
	PageHeader,
	Panel,
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
	Stack,
} from "@/shared/ui";
import { sourceLabel } from "../model/source-label";
import { useReconcileSource } from "../service/mutations";
import {
	useSourceTriggerDeclaration,
	useTriggerSources,
} from "../service/queries";
import { AccessSection } from "./access-section";
import { RunsSection, TechnicalDetails } from "./runs-section";
import { ScheduleSection } from "./schedule-section";

// The rows when there is no answer: one array for every render.
const NO_ACCESSES: TriggerDelegationReference[] = [];
const NO_RUNS: TriggerChildContext[] = [];

/**
 * Unattended work, for an organization administrator: the work that runs for a
 * connected data source on a schedule with nobody signed in, whose access that
 * work uses, and the runs on record.
 *
 * Every act here is the platform's own operation and none is a simulation:
 * "Run now" admits the same work a scheduled run does, and stopping an access
 * is refused work from the next run on.
 */
export function UnattendedWorkPage() {
	const { organizationId: orgId = "" } = useAuth();
	const [selected, setSelected] = useState("");
	const sources = useTriggerSources(orgId);
	const sourcesOutcome = readOutcome(sources.isError, sources.error);
	const rows = mayKeepRetainedRows(sourcesOutcome) ? (sources.data ?? []) : [];
	const sourcesStale = staleReadNotice(
		sourcesOutcome,
		"this organization's data sources",
	);
	const sourcesWaiting = sources.isPending && sources.fetchStatus !== "idle";
	const source = rows.find((candidate) => candidate.id === selected) ?? rows[0];
	const sourceId = source?.id ?? "";
	const sourceName = source ? sourceLabel(source) : "";

	const read = useSourceTriggerDeclaration(orgId, sourceId);
	const run = useReconcileSource(orgId);
	const outcome = readOutcome(read.isError, read.error);
	// A refused read takes what was on screen with it; a failed one leaves it,
	// with a note that it may be out of date.
	const answer = mayKeepRetainedRows(outcome) ? read.data : undefined;
	const waiting = read.isPending && read.fetchStatus !== "idle";
	const accesses = answer?.delegations ?? NO_ACCESSES;
	const runs = answer?.children ?? NO_RUNS;
	const canRun = accesses.some((access) => access.active);

	return (
		<PageBody>
			<PageHeader
				className="flex-wrap"
				title="Unattended work"
				description="Work that runs on a schedule with nobody signed in, such as refreshing a connected data source, and whose access that work uses."
				actions={
					<>
						<OrgSelector />
						{source && (
							<Button
								disabled={run.isPending || !canRun}
								title={
									canRun
										? undefined
										: "Set up someone's access first: without it a run is refused."
								}
								onClick={() =>
									run.mutate(sourceId, {
										onSuccess: () =>
											toast.success(`Run started for ${sourceName}`),
										onError: (error) =>
											toast.error(failureText("start a run", error)),
									})
								}
							>
								<Play className="h-4 w-4" />
								{run.isPending ? "Starting…" : "Run now"}
							</Button>
						)}
					</>
				}
			/>

			{sourcesStale && rows.length > 0 && (
				<Banner tone="warning">{sourcesStale}</Banner>
			)}

			{sourcesWaiting ? (
				// The kit's never-flash wait: nothing for the first 200ms, then an
				// indicator that stays long enough to read.
				<DelayedLoading
					active
					label="Loading this organization's data sources"
				/>
			) : !source ? (
				sourcesOutcome === "empty" ? (
					<EmptyState
						icon={<Database />}
						heading="No data source is connected"
						description="Unattended work runs for a connected data source, such as refreshing it on a schedule. Connect one and its schedule, and whose access it uses, appear here."
					>
						<Button
							variant="outline"
							nativeButton={false}
							render={<Link href="/admin/datasources" />}
						>
							Connect a data source
						</Button>
					</EmptyState>
				) : (
					<ReadFailure
						outcome={sourcesOutcome}
						what="this organization's data sources"
						onRetry={() => void sources.refetch()}
					/>
				)
			) : (
				<>
					<Field
						label="Data source"
						description="Each connected source has its own schedule and its own access."
						className="w-full sm:w-96"
					>
						{(control) => (
							<Select
								value={sourceId}
								onValueChange={(value) => setSelected(value ?? "")}
							>
								<SelectTrigger id={control.id} className="w-full">
									{/* The label is supplied here rather than left to the
									    popup: a trigger that read its own value would show a
									    raw source id until the popup mounted. */}
									<SelectValue placeholder="Select a data source">
										{sourceName}
									</SelectValue>
								</SelectTrigger>
								<SelectContent>
									{rows.map((candidate) => (
										<SelectItem key={candidate.id} value={candidate.id}>
											{sourceLabel(candidate)}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						)}
					</Field>

					{outcome !== "empty" && !answer ? (
						<ReadFailure
							outcome={outcome}
							what={`unattended work for ${sourceName}`}
							onRetry={() => void read.refetch()}
						/>
					) : (
						<>
							{outcome === "failed" && (
								<Banner tone="warning">
									{staleReadNotice(
										outcome,
										`unattended work for ${sourceName}`,
									)}
								</Banner>
							)}
							<ScheduleSection
								declaration={answer?.declaration}
								isLoading={waiting}
							/>
							<AccessSection
								orgId={orgId}
								sourceId={sourceId}
								sourceName={sourceName}
								accesses={accesses}
								isLoading={waiting}
							/>
							<RunsSection runs={runs} isLoading={waiting} canRun={canRun} />
							{answer && (
								<TechnicalDetails
									declaration={answer.declaration}
									accesses={accesses}
									runs={runs}
								/>
							)}
						</>
					)}
				</>
			)}
		</PageBody>
	);
}

/**
 * A read that gave no answer. A missing role is not fixed by trying again, so
 * that one names who can grant it; only a real failure offers a retry.
 */
function ReadFailure({
	outcome,
	what,
	onRetry,
}: {
	outcome: ReadOutcome;
	what: string;
	onRetry: () => void;
}) {
	if (outcome === "forbidden")
		return (
			<Panel>
				<ErrorState
					title={`You don't have access to ${what}`}
					detail="There may be some; this page can't show it to you. Ask an organization administrator to give you access."
				/>
			</Panel>
		);
	return (
		<Panel>
			<Stack direction="row" gap={4} align="center" justify="between">
				<ErrorState
					title={`Couldn't load ${what}`}
					detail="Whether there is any is unknown. If it keeps failing, tell your administrator."
				/>
				<Button variant="outline" onClick={onRetry}>
					Retry
				</Button>
			</Stack>
		</Panel>
	);
}
