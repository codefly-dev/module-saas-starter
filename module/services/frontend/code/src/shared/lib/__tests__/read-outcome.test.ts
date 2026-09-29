import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import {
	mayKeepRetainedRows,
	readOutcome,
	readOutcomeMessage,
	staleReadNotice,
} from "../read-outcome";

describe("readOutcome", () => {
	it("treats a read that succeeded as genuinely empty", () => {
		expect(readOutcome(false, undefined)).toBe("empty");
	});

	it("separates a refused read from an empty one", () => {
		expect(
			readOutcome(true, new ConnectError("nope", Code.PermissionDenied)),
		).toBe("forbidden");
	});

	it("treats any other failure as unknown rather than as refused", () => {
		expect(readOutcome(true, new ConnectError("down", Code.Unavailable))).toBe(
			"failed",
		);
		expect(readOutcome(true, new Error("network down"))).toBe("failed");
	});

	it("does not read a lapsed access token as a missing grant", () => {
		// The transport exchanges the refresh cookie and retries, and a session
		// that is genuinely gone is torn down by the auth provider. Rendering it as
		// "you don't have permission" would send an administrator looking for a
		// grant that was never the problem.
		expect(
			readOutcome(true, new ConnectError("expired", Code.Unauthenticated)),
		).toBe("failed");
	});
});

describe("readOutcomeMessage", () => {
	it("uses the caller's own words when there is genuinely nothing", () => {
		expect(
			readOutcomeMessage("empty", "this team's roles", "No roles yet."),
		).toBe("No roles yet.");
	});

	it("says a refusal is about the reader, not about the subject", () => {
		const message = readOutcomeMessage(
			"forbidden",
			"who holds this role",
			"Nobody holds this role.",
		);
		expect(message).toContain("don't have permission");
		// The defect this exists to prevent: the empty sentence must not appear.
		expect(message).not.toContain("Nobody holds this role.");
	});

	it("says a failed read leaves the answer unknown", () => {
		const message = readOutcomeMessage(
			"failed",
			"this organization's members",
			"No members in this organization.",
		);
		expect(message).toContain("unknown");
		expect(message).not.toContain("No members in this organization.");
	});
});

// TanStack keeps the last successful answer when a refetch rejects, so "are
// there rows" and "did the last read succeed" are separate questions and a
// surface that only asks the first never reaches its denial branch.
describe("what survives a failed refresh", () => {
	it("drops rows the reader may no longer be allowed to see", () => {
		expect(mayKeepRetainedRows("forbidden")).toBe(false);
	});

	it("keeps rows through a transient failure, which says nothing about authority", () => {
		// Blanking a good table because one request did not come back is worse
		// than the staleness it would be hiding.
		expect(mayKeepRetainedRows("failed")).toBe(true);
		expect(mayKeepRetainedRows("empty")).toBe(true);
	});

	it("says retained rows may be out of date, and only then", () => {
		expect(staleReadNotice("failed", "this team's roles")).toContain(
			"may be out of date",
		);
		// Nothing to caveat: the rows are gone, or they are current.
		expect(staleReadNotice("forbidden", "this team's roles")).toBeNull();
		expect(staleReadNotice("empty", "this team's roles")).toBeNull();
	});
});
