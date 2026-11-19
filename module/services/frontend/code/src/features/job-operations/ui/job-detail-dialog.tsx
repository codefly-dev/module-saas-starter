"use client";

import { RotateCcw } from "lucide-react";
import type { JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	Section,
	Spinner,
	Stack,
	useLoadingPhase,
} from "@/shared/ui";
import { useJob } from "../service/queries";
import { JobAttemptsTable } from "./job-attempts-table";
import { JobFacts } from "./job-facts";
import { JobHistoryTable } from "./job-history-table";
import { ReadFailure } from "./read-failure";

/**
 * One job's record: what it is, each time it was tried and each change of
 * state. A job that failed for good can be run again from here.
 */
export function JobDetailDialog({
	jobId,
	onClose,
	onReplay,
}: {
	/** The job to show; `null` keeps the dialog closed. */
	jobId: string | null;
	onClose: () => void;
	onReplay: (job: JobSummary) => void;
}) {
	const detail = useJob(jobId);
	// A disabled query sits at `isPending` without a read in flight.
	const waiting = detail.isPending && detail.fetchStatus !== "idle";
	const { indicator } = useLoadingPhase(waiting);
	const job = detail.data?.job;

	return (
		<Dialog open={jobId !== null} onOpenChange={(open) => !open && onClose()}>
			<DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-3xl">
				<DialogHeader>
					<DialogTitle>Job details</DialogTitle>
					<DialogDescription>
						The job&apos;s history as it was recorded. What it carries is never
						shown.
					</DialogDescription>
				</DialogHeader>
				{detail.isError ? (
					<ReadFailure
						subject="this job"
						error={detail.error}
						onRetry={() => void detail.refetch()}
					/>
				) : indicator ? (
					<Spinner label="Loading the job" />
				) : job && detail.data ? (
					<Stack gap={6}>
						<JobFacts job={job} />
						<Section
							title="Tries"
							description="Each time a worker picked the job up."
						>
							<JobAttemptsTable attempts={detail.data.attempts} />
						</Section>
						<Section
							title="History"
							description="Each change of state, oldest first."
						>
							<JobHistoryTable transitions={detail.data.transitions} />
						</Section>
					</Stack>
				) : null}
				<DialogFooter>
					<Button variant="outline" onClick={onClose}>
						Close
					</Button>
					{job?.state === JobState.DEAD_LETTER && !detail.isError && (
						<Button onClick={() => onReplay(job)}>
							<RotateCcw />
							Run this job again
						</Button>
					)}
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
