import { JobAttemptOutcome, JobState } from "@/gen/saas/jobs/v1/jobs_pb";

/** What a status says; the kit's Badge `tone`. */
export type StatusTone = "neutral" | "success" | "warning" | "danger" | "info";

/** The states a person can filter the jobs list by, in the order a job moves. */
export const JOB_STATE_FILTERS = [
	JobState.PENDING,
	JobState.PROCESSING,
	JobState.RETRYING,
	JobState.SUCCEEDED,
	JobState.DEAD_LETTER,
	JobState.CANCELED,
] as const;

const STATE_LABELS: Record<JobState, string> = {
	[JobState.PENDING]: "Pending",
	[JobState.PROCESSING]: "Processing",
	[JobState.RETRYING]: "Retrying",
	[JobState.SUCCEEDED]: "Succeeded",
	[JobState.DEAD_LETTER]: "Failed for good",
	[JobState.CANCELED]: "Canceled",
	[JobState.UNSPECIFIED]: "Unknown",
};

/** A state a newer server sends that this build has no word for reads "Unknown". */
export function jobStateLabel(state: JobState): string {
	return STATE_LABELS[state] ?? "Unknown";
}

const STATE_TONES: Record<JobState, StatusTone> = {
	[JobState.PENDING]: "neutral",
	[JobState.PROCESSING]: "info",
	[JobState.RETRYING]: "warning",
	[JobState.SUCCEEDED]: "success",
	[JobState.DEAD_LETTER]: "danger",
	[JobState.CANCELED]: "neutral",
	[JobState.UNSPECIFIED]: "neutral",
};

export function jobStateTone(state: JobState): StatusTone {
	return STATE_TONES[state] ?? "neutral";
}

/** One line of a job's history: where it was and where it went. */
export function jobTransitionLabel(from: JobState, to: JobState): string {
	if (from === JobState.UNSPECIFIED) {
		return `Created as ${jobStateLabel(to).toLowerCase()}`;
	}
	return `${jobStateLabel(from)} to ${jobStateLabel(to).toLowerCase()}`;
}

const OUTCOME_LABELS: Record<JobAttemptOutcome, string> = {
	[JobAttemptOutcome.UNSPECIFIED]: "In progress",
	[JobAttemptOutcome.SUCCEEDED]: "Succeeded",
	[JobAttemptOutcome.RETRYABLE_FAILURE]: "Failed, will be retried",
	[JobAttemptOutcome.PERMANENT_FAILURE]: "Failed for good",
	[JobAttemptOutcome.LEASE_EXPIRED]: "Stalled, the worker stopped responding",
	[JobAttemptOutcome.CANCELED]: "Canceled",
};

/** How one attempt ended. An attempt with no recorded end is still running. */
export function attemptOutcomeLabel(outcome: JobAttemptOutcome): string {
	return OUTCOME_LABELS[outcome] ?? "Ended in a way this page cannot describe";
}

const OUTCOME_TONES: Record<JobAttemptOutcome, StatusTone> = {
	[JobAttemptOutcome.UNSPECIFIED]: "info",
	[JobAttemptOutcome.SUCCEEDED]: "success",
	[JobAttemptOutcome.RETRYABLE_FAILURE]: "warning",
	[JobAttemptOutcome.PERMANENT_FAILURE]: "danger",
	[JobAttemptOutcome.LEASE_EXPIRED]: "warning",
	[JobAttemptOutcome.CANCELED]: "neutral",
};

export function attemptOutcomeTone(outcome: JobAttemptOutcome): StatusTone {
	return OUTCOME_TONES[outcome] ?? "neutral";
}
