import { describe, expect, it } from "vitest";
import { JobAttemptOutcome, JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import {
	attemptOutcomeLabel,
	attemptOutcomeTone,
	JOB_STATE_FILTERS,
	jobStateLabel,
	jobStateTone,
	jobTransitionLabel,
} from "./job-state";

const RAW = /[A-Z]{2,}_|_[a-z]/;

describe("jobStateLabel", () => {
	it("covers every generated durable state", () => {
		expect(JOB_STATE_FILTERS.map(jobStateLabel)).toEqual([
			"Pending",
			"Processing",
			"Retrying",
			"Succeeded",
			"Failed for good",
			"Canceled",
		]);
	});

	it("reads a state this build does not know as Unknown, not a number", () => {
		expect(jobStateLabel(99 as JobState)).toBe("Unknown");
		expect(jobStateLabel(JobState.UNSPECIFIED)).toBe("Unknown");
		expect(jobStateTone(99 as JobState)).toBe("neutral");
	});

	it("tells success, trouble and failure apart by tone", () => {
		expect(jobStateTone(JobState.SUCCEEDED)).toBe("success");
		expect(jobStateTone(JobState.RETRYING)).toBe("warning");
		expect(jobStateTone(JobState.DEAD_LETTER)).toBe("danger");
	});
});

describe("jobTransitionLabel", () => {
	it("reads the first transition as a creation", () => {
		expect(jobTransitionLabel(JobState.UNSPECIFIED, JobState.PENDING)).toBe(
			"Created as pending",
		);
	});

	it("names both ends of a later change", () => {
		expect(jobTransitionLabel(JobState.PROCESSING, JobState.DEAD_LETTER)).toBe(
			"Processing to failed for good",
		);
	});
});

describe("attemptOutcomeLabel", () => {
	it("has plain words for every outcome and never an enum name", () => {
		const outcomes = [
			JobAttemptOutcome.UNSPECIFIED,
			JobAttemptOutcome.SUCCEEDED,
			JobAttemptOutcome.RETRYABLE_FAILURE,
			JobAttemptOutcome.PERMANENT_FAILURE,
			JobAttemptOutcome.LEASE_EXPIRED,
			JobAttemptOutcome.CANCELED,
		];
		const labels = outcomes.map(attemptOutcomeLabel);
		expect(labels).toEqual([
			"In progress",
			"Succeeded",
			"Failed, will be retried",
			"Failed for good",
			"Stalled, the worker stopped responding",
			"Canceled",
		]);
		for (const label of labels) expect(label).not.toMatch(RAW);
		expect(attemptOutcomeTone(JobAttemptOutcome.PERMANENT_FAILURE)).toBe(
			"danger",
		);
	});

	it("gives an unknown outcome a plain sentence", () => {
		expect(attemptOutcomeLabel(42 as JobAttemptOutcome)).toBe(
			"Ended in a way this page cannot describe",
		);
		expect(attemptOutcomeTone(42 as JobAttemptOutcome)).toBe("neutral");
	});
});
