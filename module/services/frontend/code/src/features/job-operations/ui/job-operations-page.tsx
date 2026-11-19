"use client";

import { RefreshCw } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import type { JobState, JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { readOutcome } from "@/shared/lib/read-outcome";
import { Button, Page as PageBody, PageHeader, Section } from "@/shared/ui";
import {
	FIRST_PAGE,
	nextPage,
	type PageCursor,
	pageNumber,
	previousPage,
} from "../model/page-cursor";
import { queueTotals } from "../model/queue-totals";
import { useJobOperations, useJobs } from "../service/queries";
import { JobDetailDialog } from "./job-detail-dialog";
import { JobsFilters } from "./jobs-filters";
import { JobsTable } from "./jobs-table";
import { QueueSummaryTiles } from "./queue-summary-tiles";
import { QueuesTable } from "./queues-table";
import { ReplayJobDialog } from "./replay-job-dialog";

export function JobOperationsPage() {
	const [queue, setQueue] = useState("");
	const [state, setState] = useState<JobState | undefined>();
	const [cursor, setCursor] = useState<PageCursor>(FIRST_PAGE);
	const [selectedJobId, setSelectedJobId] = useState<string | null>(null);
	const [replayTarget, setReplayTarget] = useState<JobSummary | null>(null);

	const operations = useJobOperations(queue);
	const jobs = useJobs({ queue, state, pageToken: cursor.token });

	const operationsRefused =
		readOutcome(operations.isError, operations.error) === "forbidden";
	const queues = operationsRefused ? undefined : operations.data?.queues;
	const totals = useMemo(
		() => (queues ? queueTotals(queues) : undefined),
		[queues],
	);

	// A new filter is a new list, so it starts from its first page.
	function filterByQueue(next: string) {
		setQueue(next);
		setCursor(FIRST_PAGE);
	}
	function filterByState(next: JobState | undefined) {
		setState(next);
		setCursor(FIRST_PAGE);
	}
	function clearFilters() {
		setQueue("");
		setState(undefined);
		setCursor(FIRST_PAGE);
	}
	const inspect = useCallback(
		(job: JobSummary) => setSelectedJobId(job.id),
		[],
	);

	return (
		<PageBody>
			<PageHeader
				title="Job operations"
				description="How the platform's background work queues are doing, and the history of each job. What a job carries is never shown here."
				actions={
					<Button
						variant="outline"
						onClick={() => Promise.all([operations.refetch(), jobs.refetch()])}
						disabled={operations.isFetching || jobs.isFetching}
					>
						<RefreshCw />
						Refresh
					</Button>
				}
			/>

			<QueueSummaryTiles
				totals={totals}
				isLoading={operations.isLoading}
				forbidden={operationsRefused}
			/>

			<Section
				title="Queues"
				description="How many jobs each queue holds right now."
			>
				<QueuesTable
					queues={queues}
					isLoading={operations.isLoading}
					isError={operations.isError}
					error={operations.error}
					onRetry={() => void operations.refetch()}
					queueFilter={queue}
					onClearFilter={() => filterByQueue("")}
				/>
			</Section>

			<Section
				title="Jobs"
				description="Where each job went, how many times it was tried and why it failed. Choosing a queue narrows the counts above as well."
			>
				<JobsFilters
					queue={queue}
					state={state}
					onQueueChange={filterByQueue}
					onStateChange={filterByState}
					onClear={clearFilters}
				/>
				<JobsTable
					jobs={jobs.data?.jobs}
					isLoading={jobs.isLoading}
					isError={jobs.isError}
					error={jobs.error}
					onRetry={() => void jobs.refetch()}
					filtered={queue !== "" || state !== undefined}
					onClearFilters={clearFilters}
					onInspect={inspect}
					page={pageNumber(cursor)}
					hasPrevious={cursor.previous.length > 0}
					hasNext={Boolean(jobs.data?.nextPageToken)}
					onPrevious={() => setCursor(previousPage)}
					onNext={() =>
						setCursor((current) =>
							nextPage(current, jobs.data?.nextPageToken ?? ""),
						)
					}
				/>
			</Section>

			<JobDetailDialog
				jobId={selectedJobId}
				onClose={() => setSelectedJobId(null)}
				onReplay={setReplayTarget}
			/>
			<ReplayJobDialog
				job={replayTarget}
				onClose={() => setReplayTarget(null)}
				onReplayed={() => {
					setReplayTarget(null);
					setSelectedJobId(null);
				}}
			/>
		</PageBody>
	);
}
