import { describe, expect, it } from "vitest";
import { JobState } from "@/gen/saas/jobs/v1/jobs_pb";
import { attemptsUsed, jobFailureWords, jobRetryOutlook } from "./job-failure";

// Codes the accounts service emits today (worker, events, installation,
// privacy, data sources, email, webhooks, billing, audit).
const KNOWN_CODES = [
	"jobs.handler_failed",
	"jobs.handler_panic",
	"jobs.lease_expired",
	"lease_expired",
	"events_nack",
	"installation.refused",
	"installation.unavailable",
	"privacy.workflow_failed",
	"privacy.workflow_unavailable",
	"audit_archive.not_configured",
	"audit_archive.receipt_failed",
	"audit.invalid_job",
	"billing.invalid_job",
	"datasource.budget_exhausted",
	"datasource.credential_rejected",
	"datasource.credential_service_unavailable",
	"datasource.credential_unreadable",
	"datasource.github_rate_limited",
	"datasource.github_not_found",
	"datasource.github_app_unconfigured",
	"datasource.repository_too_large",
	"datasource.malformed_delivery",
	"email.provider_rejected",
	"webhooks.delivery_missing",
	"webhooks.subscription_inactive",
];

describe("jobFailureWords", () => {
	it("never puts a code, or a part of one, on screen", () => {
		for (const code of [...KNOWN_CODES, "brand_new.never_seen_before", ""]) {
			const words = jobFailureWords(code);
			for (const text of [words.summary, words.advice]) {
				expect(text, code).not.toMatch(/_|\w\.\w/);
				expect(text.length, code).toBeGreaterThan(10);
			}
			if (code) expect(words.summary).not.toContain(code);
		}
	});

	it("says what was being done and why it stopped", () => {
		expect(jobFailureWords("jobs.handler_failed").summary).toBe(
			"The job stopped with an error",
		);
		expect(jobFailureWords("jobs.handler_panic").summary).toBe(
			"The job crashed unexpectedly",
		);
		expect(jobFailureWords("events_nack").summary).toBe(
			"Delivering an event to a subscriber was turned away by the subscriber",
		);
		expect(jobFailureWords("datasource.github_rate_limited").summary).toBe(
			"Syncing a data source was stopped by a usage limit",
		);
		expect(jobFailureWords("email.provider_rejected").summary).toBe(
			"Sending an email was refused access",
		);
		expect(jobFailureWords("lease_expired").summary).toBe(
			"The job stalled because the worker running it stopped responding",
		);
	});

	it("reads an unreachable credential service as an outage, not a refusal", () => {
		expect(
			jobFailureWords("datasource.credential_service_unavailable").summary,
		).toBe("Syncing a data source could not reach a service it depends on");
	});

	it("tells the operator what they can do", () => {
		expect(jobFailureWords("datasource.github_rate_limited").advice).toMatch(
			/Wait for the limit to reset/,
		);
		expect(jobFailureWords("billing.invalid_job").advice).toMatch(
			/will fail the same way/,
		);
	});

	it("keeps the area when only the reason is new, and the reason when only the area is", () => {
		expect(jobFailureWords("email.something_new").summary).toBe(
			"Sending an email failed",
		);
		expect(jobFailureWords("newmodule.rate_limited").summary).toBe(
			"The job was stopped by a usage limit",
		);
	});

	it("gives an unknown code a plain generic sentence", () => {
		const unknown = jobFailureWords("zzz.qqq");
		expect(unknown.summary).toBe(
			"The job failed for a reason this page has no description for",
		);
		expect(unknown.advice).toMatch(/Tell the team that owns this work/);
		expect(jobFailureWords("  ")).toEqual(unknown);
	});
});

describe("jobRetryOutlook", () => {
	it("says how many automatic tries are left while retrying", () => {
		expect(jobRetryOutlook(JobState.RETRYING, 2, 5)).toBe(
			"It will be tried again automatically, up to 3 more times.",
		);
		expect(jobRetryOutlook(JobState.RETRYING, 4, 5)).toBe(
			"It will be tried again automatically, one more time.",
		);
	});

	it("says a job that failed for good will not retry on its own", () => {
		expect(jobRetryOutlook(JobState.DEAD_LETTER, 5, 5)).toBe(
			"Every try has been used, so it will not be tried again on its own.",
		);
	});

	it("covers the remaining states without promising a retry", () => {
		expect(jobRetryOutlook(JobState.SUCCEEDED, 2, 5)).toMatch(/succeeded/);
		expect(jobRetryOutlook(JobState.CANCELED, 1, 5)).toMatch(/canceled/);
		expect(jobRetryOutlook(JobState.PROCESSING, 1, 5)).toMatch(/right now/);
		expect(jobRetryOutlook(JobState.UNSPECIFIED, 0, 0)).toMatch(/not known/);
	});
});

describe("attemptsUsed", () => {
	it("reads as a count out of a limit", () => {
		expect(attemptsUsed(2, 5)).toBe("2 of 5");
	});
});
