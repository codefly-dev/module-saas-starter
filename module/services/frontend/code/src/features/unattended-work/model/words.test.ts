import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";
import { formatDate } from "@/shared/lib/utils";
import {
	missedRunWords,
	momentWords,
	overlapWords,
	permissionsWords,
	permissionWords,
	runKindWords,
	runsWithWords,
	scheduleWords,
	timeLimitWords,
	workWords,
} from "./words";

describe("scheduleWords", () => {
	it.each([
		["*/30 * * * *", "Every 30 minutes"],
		["*/1 * * * *", "Every minute"],
		["* * * * *", "Every minute"],
		["0 * * * *", "Every hour"],
		["15 */6 * * *", "Every 6 hours"],
		["5 9 * * *", "Every day at 09:05"],
		["0 18 * * 1", "Every Monday at 18:00"],
		["0 18 * * 7", "Every Sunday at 18:00"],
		["0 0 1 * *", "On a custom schedule"],
		["0 9-17 * * 1-5", "On a custom schedule"],
		["@hourly", "Not scheduled"],
		["", "Not scheduled"],
		[undefined, "Not scheduled"],
	])("reads %s as %s", (cron, words) => {
		expect(scheduleWords(cron)).toBe(words);
	});
});

describe("policy words", () => {
	it("says what happens to a missed run", () => {
		expect(missedRunWords("skip")).toContain("Skipped");
		expect(missedRunWords("catch_up")).toContain("as soon as possible");
	});

	it("says what happens when runs overlap", () => {
		expect(overlapWords("skip")).toBe("The new run is skipped.");
		expect(overlapWords("queue")).toContain("waits");
		expect(overlapWords("cancel_previous")).toContain(
			"previous run is stopped",
		);
	});

	it.each(["MISSED_RUN_POLICY_SKIP", "cancel_previous_v2", "", undefined])(
		"never shows a value it does not know: %s",
		(value) => {
			expect(missedRunWords(value)).toBe("Not specified");
			expect(overlapWords(value)).toBe("Not specified");
		},
	);
});

describe("timeLimitWords", () => {
	const seconds = (n: number) => BigInt(n) * BigInt(1_000_000_000);
	it("reads nanoseconds as a duration a person says", () => {
		expect(timeLimitWords(seconds(600))).toBe("10 minutes");
		expect(timeLimitWords(seconds(60))).toBe("1 minute");
		expect(timeLimitWords(seconds(7200))).toBe("2 hours");
		expect(timeLimitWords(seconds(45))).toBe("45 seconds");
		expect(timeLimitWords(BigInt(0))).toBe("No limit set");
		expect(timeLimitWords(undefined)).toBe("No limit set");
	});
});

describe("workWords and runsWithWords", () => {
	it("names the refresh, and nothing else by its technical name", () => {
		expect(
			workWords("/saas.accounts.v1.DatasourceService/ReconcileSource"),
		).toBe("Refreshes this data source so its content stays up to date");
		expect(workWords("/acme.v1.Other/Thing")).toBe(
			"Scheduled work for this data source",
		);
		expect(workWords(undefined)).toBe("Scheduled work for this data source");
	});

	it("says whose access a run uses, and says so when it is nobody's", () => {
		expect(runsWithWords({ source: "s", binding: "" })).toBe(
			"A person's access, listed below",
		);
		expect(runsWithWords({ source: "", binding: "b" })).toBe(
			"The module's own access",
		);
		expect(runsWithWords(undefined)).toBe(
			"Nobody's access, so runs are refused",
		);
	});
});

describe("permission words", () => {
	it.each([
		["collections:write", "Change collections"],
		["collections:read", "View collections"],
		["data_sources:invoke", "Run data sources"],
		["audit-log:delete", "Delete audit log"],
		["collections:frobnicate", "Other access"],
		["collections", "Other access"],
		["a:b:c", "Other access"],
		["COLLECTIONS:write", "Other access"],
		["", "Other access"],
	])("reads %s as %s", (scope, words) => {
		expect(permissionWords(scope)).toBe(words);
	});

	it("lists each thing once, and says when there is nothing", () => {
		expect(
			permissionsWords(["collections:write", "collections:read", "x", "y"]),
		).toBe("Change collections, View collections, Other access");
		expect(permissionsWords([])).toBe("Nothing");
	});
});

describe("runs", () => {
	it("says what a recorded run was for", () => {
		expect(runKindWords(false)).toBe("Did the work");
		expect(runKindWords(true)).toBe("Checked the result of an earlier run");
	});

	it("reads a moment as a date, and an absent one as the caller's words", () => {
		const at = new Date("2026-10-12T09:05:00Z");
		expect(momentWords(timestampFromDate(at), "Not yet")).toBe(
			formatDate(at.toISOString()),
		);
		expect(momentWords(undefined, "Not yet")).toBe("Not yet");
	});
});
