import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";

// A job's failure arrives as a code ("datasource.github_rate_limited") and a
// message written for an engineer. Neither is shown: the code is read here into
// what was being done and why it stopped, in words an operator can act on, and
// the message is never read at all.
//
// Codes are an open set owned by whichever service ran the job, shaped
// `<area>.<reason>`. So the reading is by part rather than by an exhaustive
// table: the area says what was being done, the reason says why it stopped, and
// a code this page has never seen still gets a plain sentence.

export interface JobFailureWords {
	/** One line for a table cell: what was being done and why it stopped. */
	summary: string;
	/** What an operator can do about it. */
	advice: string;
}

const AREAS: Record<string, string> = {
	jobs: "The job",
	events: "Delivering an event to a subscriber",
	events_nack: "Delivering an event to a subscriber",
	datasource: "Syncing a data source",
	email: "Sending an email",
	webhooks: "Delivering a webhook",
	billing: "A billing update",
	audit: "Recording the audit log",
	audit_archive: "Archiving the audit log",
	installation: "An installation step",
	privacy: "A privacy request",
};

interface Reason {
	matches: RegExp;
	why: string;
	advice: string;
}

const FIX_THEN_RERUN =
	"Fix the cause, then run the job again if it has failed for good.";

// First match wins, so the specific readings sit above the general ones.
const REASONS: Reason[] = [
	{
		matches: /lease_expired/,
		why: "stalled because the worker running it stopped responding",
		advice:
			"Nothing to do unless it keeps happening; another worker picks the job up.",
	},
	{
		matches: /panic/,
		why: "crashed unexpectedly",
		advice:
			"Tell the team that owns this work. Running it again is likely to crash the same way.",
	},
	{
		matches: /nack/,
		why: "was turned away by the subscriber",
		advice:
			"Check that the subscriber is running and accepting events, then run the job again if it has failed for good.",
	},
	{
		matches: /rate_limit|budget_exhausted/,
		why: "was stopped by a usage limit",
		advice:
			"Wait for the limit to reset. Run the job again later if it has failed for good.",
	},
	{
		matches: /not_configured|unconfigured/,
		why: "could not start because a required setting is missing",
		advice: "Finish the setup it depends on, then run the job again.",
	},
	{
		matches: /unavailable|incomplete/,
		why: "could not reach a service it depends on",
		advice:
			"Usually temporary. If it has failed for good, run the job again once the service is back.",
	},
	{
		matches:
			/credential|unreadable|unauthorized|access_denied|scope_denied|reauth|refused|rejected|revoked/,
		why: "was refused access",
		advice:
			"Check the connection's sign-in and permissions, then run the job again if it has failed for good.",
	},
	{
		matches: /not_found|missing|not_visible|inactive|not_github/,
		why: "needed something that is gone or switched off",
		advice:
			"Running it again will fail the same way until what it needs is restored.",
	},
	{
		matches: /too_large|too_many/,
		why: "had more to process than is allowed",
		advice:
			"Running it again will fail the same way. Reduce what it has to process first.",
	},
	{
		matches: /invalid|malformed/,
		why: "was given instructions it could not use",
		advice:
			"Running it again will fail the same way. Tell the team that owns this work.",
	},
	{
		matches: /failed/,
		why: "stopped with an error",
		advice: FIX_THEN_RERUN,
	},
];

const UNKNOWN: JobFailureWords = {
	summary: "The job failed for a reason this page has no description for",
	advice:
		"Tell the team that owns this work. You can run the job again if it has failed for good.",
};

/** Plain words for a failure code. Never returns the code itself. */
export function jobFailureWords(code: string): JobFailureWords {
	const normalized = code.trim().toLowerCase();
	if (!normalized) return UNKNOWN;
	const separator = normalized.indexOf(".");
	const area =
		AREAS[separator === -1 ? normalized : normalized.slice(0, separator)];
	const reason = REASONS.find((entry) => entry.matches.test(normalized));
	if (!area && !reason) return UNKNOWN;
	return {
		summary: `${area ?? "The job"} ${reason?.why ?? "failed"}`,
		advice: reason?.advice ?? FIX_THEN_RERUN,
	};
}

/**
 * Whether a job that has failed will be tried again, given where it stands.
 * `attemptCount` of `maxAttempts` have been used.
 */
export function jobRetryOutlook(
	state: JobState,
	attemptCount: number,
	maxAttempts: number,
): string {
	const left = Math.max(maxAttempts - attemptCount, 0);
	switch (state) {
		case JobState.RETRYING:
		case JobState.PENDING:
			return left === 1
				? "It will be tried again automatically, one more time."
				: left > 1
					? `It will be tried again automatically, up to ${left} more times.`
					: "It will be tried again automatically.";
		case JobState.PROCESSING:
			return "It is being tried again right now.";
		case JobState.SUCCEEDED:
			return "A later try succeeded, so there is nothing to do.";
		case JobState.DEAD_LETTER:
			return "Every try has been used, so it will not be tried again on its own.";
		case JobState.CANCELED:
			return "The job was canceled, so it will not be tried again.";
		default:
			return "Whether it will be tried again is not known.";
	}
}

/** "2 of 5": how many tries a job has used. */
export function attemptsUsed(
	attemptCount: number,
	maxAttempts: number,
): string {
	return `${attemptCount} of ${maxAttempts}`;
}
