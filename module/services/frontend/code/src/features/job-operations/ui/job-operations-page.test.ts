import { describe, expect, it } from "vitest";
import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { jobStateLabel } from "./job-operations-page";

describe("jobStateLabel", () => {
	it("covers every generated durable state", () => {
		expect(
			[
				JobState.PENDING,
				JobState.PROCESSING,
				JobState.RETRYING,
				JobState.SUCCEEDED,
				JobState.DEAD_LETTER,
				JobState.CANCELED,
			].map(jobStateLabel),
		).toEqual([
			"Pending",
			"Processing",
			"Retrying",
			"Succeeded",
			"Dead letter",
			"Canceled",
		]);
	});
});
