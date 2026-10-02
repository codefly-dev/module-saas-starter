import { describe, expect, it } from "vitest";
import {
	DEFAULT_STALL_AFTER_MS,
	describeSync,
	SYNC_STEPS,
} from "../sync-progress-model.js";
import type { SourceSyncView } from "../types.js";

const NOW = Date.parse("2026-09-28T12:00:00.000Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

function sync(overrides: Partial<SourceSyncView> = {}): SourceSyncView {
	return {
		jobId: "11111111-1111-1111-1111-111111111111",
		phase: "queued",
		trigger: "manual",
		queuedAt: ago(1_000),
		attempt: 1,
		maxAttempts: 5,
		...overrides,
	};
}

describe("describeSync", () => {
	it("reports a freshly queued sync as queued and still active", () => {
		const report = describeSync(sync(), { now: NOW });

		expect(report.state).toBe("queued");
		expect(report.active).toBe(true);
		expect(report.headline).toBe("Accepted, waiting for a worker");
		expect(report.step).toBe(1);
		expect(report.stepCount).toBe(SYNC_STEPS.length);
		// Entered, not completed: a bar at 0 has to keep meaning "the host has not
		// taken this yet", or a queued sync and an unaccepted one look identical.
		expect(report.percent).toBe(25);
		expect(report.counts).toEqual([]);
		expect(report.detail).toBeUndefined();
	});

	it("advances through fetching and compiled, and only then reports counts", () => {
		const fetching = describeSync(
			sync({ phase: "fetching", fetchingAt: ago(1_000) }),
			{ now: NOW },
		);
		expect(fetching.state).toBe("running");
		expect(fetching.headline).toBe("Fetching from the repository");
		expect(fetching.step).toBe(2);
		expect(fetching.percent).toBe(50);
		expect(fetching.counts).toEqual([]);

		const compiled = describeSync(
			sync({
				phase: "compiled",
				fetchingAt: ago(9_000),
				compiledAt: ago(1_000),
				changes: {
					files: 162,
					added: 12,
					modified: 150,
					deleted: 0,
					splitKnown: true,
					snapshot: false,
					commit: "abcdef1234567890",
				},
			}),
			{ now: NOW },
		);
		expect(compiled.state).toBe("running");
		expect(compiled.step).toBe(3);
		expect(compiled.counts).toEqual([
			{ label: "Files", value: 162 },
			{ label: "Added", value: 12 },
			{ label: "Modified", value: 150 },
			{ label: "Deleted", value: 0 },
		]);
		expect(compiled.commit).toBe("abcdef1234567890");
	});

	it("omits the added/modified/deleted split the host says it does not know", () => {
		// A snapshot taken after a force push knows how many files it is handing
		// off but not how they differ. Three zeros beside a real total would read
		// as "nothing changed" — the opposite of what a full snapshot means.
		const report = describeSync(
			sync({
				phase: "handed_off",
				handedOffAt: ago(1_000),
				changes: {
					files: 162,
					added: 0,
					modified: 0,
					deleted: 0,
					splitKnown: false,
					snapshot: true,
					commit: "abcdef1",
				},
			}),
			{ now: NOW },
		);

		expect(report.counts).toEqual([{ label: "Files", value: 162 }]);
	});

	it("reports a finished sync that handed a change set off as done", () => {
		const report = describeSync(
			sync({
				phase: "done",
				fetchingAt: ago(30_000),
				compiledAt: ago(20_000),
				handedOffAt: ago(10_000),
				finishedAt: ago(10_000),
				changes: {
					files: 3,
					added: 3,
					modified: 0,
					deleted: 0,
					splitKnown: true,
					snapshot: false,
					commit: "deadbee",
				},
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("done");
		expect(report.tone).toBe("success");
		expect(report.active).toBe(false);
		expect(report.percent).toBe(100);
		expect(report.headline).toBe("Handed off the changed files");
	});

	it("distinguishes a full snapshot from an incremental hand-off", () => {
		const report = describeSync(
			sync({
				phase: "done",
				finishedAt: ago(1_000),
				changes: {
					files: 162,
					added: 162,
					modified: 0,
					deleted: 0,
					splitKnown: true,
					snapshot: true,
					commit: "deadbee",
				},
			}),
			{ now: NOW },
		);

		expect(report.headline).toBe("Handed off a full snapshot");
	});

	it("reports a sync that handed nothing off as up to date, not as an empty success", () => {
		// The host leaves `changes` unset when the source had not moved. Rendering
		// that as "done, 0 files" is how a healthy no-op comes to look like a
		// failure, which is the whole reason this is its own state.
		const report = describeSync(
			sync({ phase: "done", finishedAt: ago(1_000) }),
			{ now: NOW },
		);

		expect(report.state).toBe("unchanged");
		expect(report.tone).toBe("success");
		expect(report.active).toBe(false);
		expect(report.headline).toBe("Up to date");
		expect(report.detail).toContain("had not changed");
	});

	it("reports a failed sync with the host's own sentence, and fills the bar", () => {
		const report = describeSync(
			sync({
				phase: "failed",
				fetchingAt: ago(60_000),
				finishedAt: ago(10_000),
				attempt: 5,
				maxAttempts: 5,
				failure: {
					reason: "credential",
					code: "datasource.credential_invalid",
					message:
						"The stored credential was refused. Reconnect the source with a new token.",
					retrying: false,
				},
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("failed");
		expect(report.tone).toBe("danger");
		expect(report.active).toBe(false);
		// Filled and red rather than stopped part-way: a bar frozen at 50% is
		// indistinguishable from one that is still moving slowly.
		expect(report.percent).toBe(100);
		expect(report.headline).toBe("Sync failed");
		expect(report.detail).toContain("Reconnect the source");
		expect(report.attempts).toBe("Attempt 5 of 5");
	});

	it("treats a failure the host has stopped retrying as failed even before the phase catches up", () => {
		// `phase` and `failure.retrying` are stamped from different records, so a
		// read can land between them. Trusting the phase alone would show a
		// finished failure as still fetching.
		const report = describeSync(
			sync({
				phase: "fetching",
				fetchingAt: ago(1_000),
				failure: {
					reason: "not_found",
					code: "datasource.repo_not_found",
					message: "The repository is gone or was renamed.",
					retrying: false,
				},
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("failed");
		expect(report.active).toBe(false);
	});

	it("reports a retrying sync as still active, naming the reason and the wait", () => {
		const report = describeSync(
			sync({
				phase: "fetching",
				fetchingAt: ago(1_000),
				attempt: 2,
				maxAttempts: 5,
				failure: {
					reason: "rate_limited",
					code: "datasource.github_rate_limited",
					message: "GitHub rate-limited this sync.",
					retrying: true,
					retryAt: new Date(NOW + 300_000).toISOString(),
				},
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("retrying");
		expect(report.tone).toBe("warning");
		expect(report.active).toBe(true);
		expect(report.headline).toBe("Retrying after a failed attempt");
		expect(report.detail).toBe(
			"GitHub rate-limited this sync. Next attempt in 5 minutes.",
		);
		expect(report.attempts).toBe("Attempt 2 of 5");
	});

	it("drops the retry wait once it is in the past rather than counting backwards", () => {
		const report = describeSync(
			sync({
				phase: "fetching",
				fetchingAt: ago(1_000),
				failure: {
					reason: "host_unavailable",
					code: "datasource.host_unavailable",
					message: "The provider could not be reached.",
					retrying: true,
					retryAt: ago(60_000),
				},
			}),
			{ now: NOW },
		);

		expect(report.detail).toBe("The provider could not be reached.");
	});

	it("reports a phase that has not advanced as stalling, still running", () => {
		// The owner's complaint was a sync that says nothing between queued and a
		// finished row. Silence has to become visible — but as "no progress", not
		// as a failure the host never declared.
		const report = describeSync(
			sync({
				phase: "fetching",
				fetchingAt: ago(DEFAULT_STALL_AFTER_MS + 1_000),
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("stalling");
		expect(report.tone).toBe("warning");
		expect(report.active).toBe(true);
		expect(report.headline).toBe("Fetching from the repository");
		expect(report.detail).toContain("No progress for 2 minutes");
		expect(report.detail).toContain("has not given up");
	});

	it("measures a stall from the latest phase the host stamped, not from the oldest", () => {
		// Measuring from `queuedAt` would report every long sync as stalled the
		// moment it passed the threshold, however recently it had advanced.
		const report = describeSync(
			sync({
				phase: "compiled",
				queuedAt: ago(3_600_000),
				fetchingAt: ago(1_800_000),
				compiledAt: ago(1_000),
			}),
			{ now: NOW },
		);

		expect(report.state).toBe("running");
	});

	it("honours a consumer's own stall threshold", () => {
		const view = sync({ phase: "fetching", fetchingAt: ago(60_000) });

		expect(describeSync(view, { now: NOW }).state).toBe("running");
		expect(describeSync(view, { now: NOW, stallAfterMs: 30_000 }).state).toBe(
			"stalling",
		);
	});

	it("places a sync whose phase it cannot name by the furthest stamp it has", () => {
		// `unknown` is what an older host's phase maps to. The bar still has to
		// show how far the sync got, or a deployment mid-upgrade shows every sync
		// at zero.
		const report = describeSync(
			sync({ phase: "unknown", fetchingAt: ago(1_000), compiledAt: ago(500) }),
			{ now: NOW },
		);

		expect(report.step).toBe(3);
		expect(report.headline).toBe("Sync in progress");
		expect(report.active).toBe(true);
	});

	it("reports nothing started when the host has stamped no phase at all", () => {
		const report = describeSync(
			{
				jobId: "j",
				phase: "unknown",
				trigger: "unknown",
				attempt: 0,
				maxAttempts: 0,
			},
			{ now: NOW },
		);

		expect(report.step).toBe(0);
		expect(report.percent).toBe(0);
		expect(report.attempts).toBeUndefined();
	});

	it("says nothing about attempts on a first attempt", () => {
		// "Attempt 1 of 5" on every healthy sync trains a reader to ignore the
		// line that matters on attempt 3.
		expect(describeSync(sync(), { now: NOW }).attempts).toBeUndefined();
	});
});
