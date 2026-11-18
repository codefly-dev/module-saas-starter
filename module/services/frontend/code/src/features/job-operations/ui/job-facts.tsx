"use client";

import { DescriptionList, Disclosure } from "@codefly-dev/ui/layout";
import { JobState, type JobSummary } from "@/gen/saas/jobs/v1/jobs_pb";
import { formatDate } from "@/shared/lib/utils";
import { Banner, Stack } from "@/shared/ui";
import {
	attemptsUsed,
	jobFailureWords,
	jobRetryOutlook,
} from "../model/job-failure";
import { JobScopeLabel } from "./job-scope-label";
import { JobStateBadge } from "./job-state-badge";

/**
 * What went wrong the last time the job was tried: what was being done, whether
 * it will be tried again, and what an operator can do. The server's own failure
 * text is written for an engineer and is not shown.
 */
function LastFailure({ job }: { job: JobSummary }) {
	if (!job.lastFailure) return null;
	const words = jobFailureWords(job.lastFailure.code);
	return (
		<Banner
			tone={job.state === JobState.DEAD_LETTER ? "danger" : "warning"}
			title={`${words.summary}.`}
		>
			{jobRetryOutlook(job.state, job.attemptCount, job.maxAttempts)}{" "}
			{words.advice}
		</Banner>
	);
}

/** One job, as facts: the last failure first, then what it is and who it is for. */
export function JobFacts({ job }: { job: JobSummary }) {
	return (
		<Stack gap={4}>
			<LastFailure job={job} />
			<DescriptionList
				items={[
					{ term: "State", value: <JobStateBadge state={job.state} /> },
					{ term: "Work", value: job.topic },
					{ term: "Queue", value: job.queue },
					{ term: "For", value: <JobScopeLabel job={job} /> },
					{ term: "Queued by", value: job.source || "Not recorded" },
					{ term: "Created", value: formatDate(job.createdAt) },
					{
						term: "Tries used",
						value: attemptsUsed(job.attemptCount, job.maxAttempts),
					},
					...(job.replayOf
						? [
								{
									term: "Run again from",
									value: "An earlier job that failed for good",
								},
							]
						: []),
				]}
			/>
			<Disclosure title="Reference details for support">
				<DescriptionList
					layout="stacked"
					items={[
						{ term: "Job reference", value: job.id },
						...(job.replayOf
							? [{ term: "Reference of the earlier job", value: job.replayOf }]
							: []),
						{
							term: "Duplicate-protection key",
							value: job.idempotencyKey || "None",
						},
						{ term: "Ordering key", value: job.orderingKey || "None" },
						{
							term: "Format",
							value: `${job.contentType || "Not recorded"}, version ${job.schemaVersion}`,
						},
					]}
				/>
			</Disclosure>
		</Stack>
	);
}
