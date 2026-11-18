import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import {
	draftChanges,
	draftProblem,
	nextRevisionDraft,
	requestSummary,
	withoutModule,
	writeRefusal,
} from "./draft";

const current = JSON.stringify({
	revision: 3,
	principals: {
		reports: { grant: { queues: ["reports.render"] } },
		documents: { grant: { queues: ["documents.index"] } },
	},
});

describe("writeRefusal", () => {
	const SERVER = "module envelope is invalid: unknown field workload";

	it.each([
		[Code.FailedPrecondition, "can't accept module permissions right now"],
		[Code.Aborted, "Another operator saved a newer version first"],
		[Code.PermissionDenied, "Only a platform super administrator"],
		[Code.InvalidArgument, "didn't accept these permissions"],
		[Code.Unavailable, "didn't answer in time"],
		[Code.DeadlineExceeded, "didn't answer in time"],
		[Code.Internal, "couldn't be saved, and nothing changed"],
		[Code.Unknown, "couldn't be saved, and nothing changed"],
	])("says code %s in plain words, never the server's", (code, words) => {
		const said = writeRefusal(new ConnectError(SERVER, code));
		expect(said).toContain(words);
		expect(said).not.toContain(SERVER);
		expect(said).not.toMatch(/envelope|principal|revision|\[[a-z_]+\]/);
	});

	it("tells a stale passkey apart from every other precondition", () => {
		expect(
			writeRefusal(new ConnectError("mfa_required", Code.FailedPrecondition)),
		).toBe(
			"Saving needs your passkey confirmed within the last 15 minutes. Confirm it (the sign-in page brings you back here), then save again.",
		);
	});

	it("says something plain about an error that is not the platform's", () => {
		const said = writeRefusal(new TypeError("Failed to fetch"));
		expect(said).toContain("couldn't be saved");
		expect(said).not.toContain("Failed to fetch");
	});
});

describe("draftProblem", () => {
	it("accepts a JSON object and nothing else", () => {
		expect(draftProblem('{"revision":1,"principals":{}}')).toBeNull();
		expect(draftProblem("  ")).toContain("can't be empty");
		expect(draftProblem("{not json")).toContain("isn't valid JSON");
		expect(draftProblem("[1]")).toContain("one JSON object");
	});
});

describe("draftChanges", () => {
	it("names the modules a save approves, revokes and changes", () => {
		const draft = JSON.stringify({
			revision: 4,
			principals: {
				reports: { grant: { queues: [] } },
				search: { grant: {} },
			},
		});
		expect(draftChanges(current, draft)).toEqual({
			approved: ["search"],
			revoked: ["documents"],
			changed: ["reports"],
		});
	});

	it("treats a first document as approving everything in it", () => {
		expect(
			draftChanges(undefined, '{"revision":1,"principals":{"reports":{}}}'),
		).toEqual({ approved: ["reports"], revoked: [], changed: [] });
	});

	it("names nothing when either side cannot be read", () => {
		const none = { approved: [], revoked: [], changed: [] };
		expect(draftChanges(current, "{not json")).toEqual(none);
		expect(draftChanges("{not json", current)).toEqual(none);
	});
});

describe("withoutModule", () => {
	it("removes one module and advances the version", () => {
		const next = withoutModule(current, BigInt(3), "reports");
		expect(JSON.parse(next ?? "")).toEqual({
			revision: 4,
			principals: { documents: { grant: { queues: ["documents.index"] } } },
		});
	});

	it("has nothing to remove without a readable document naming the module", () => {
		expect(withoutModule(undefined, BigInt(0), "reports")).toBeNull();
		expect(withoutModule("{not json", BigInt(3), "reports")).toBeNull();
		expect(withoutModule(current, BigInt(3), "search")).toBeNull();
	});
});

describe("requestSummary", () => {
	it("says the size of a request in plain words", () => {
		expect(
			requestSummary(
				JSON.stringify({
					principal: "reports",
					bindings: { render: {}, export: {} },
					queues: ["reports.render"],
					namespaces: ["reports"],
					resource_kinds: ["report", "chart"],
				}),
			),
		).toEqual([
			"Act for organizations in 2 ways",
			"Run background work on 1 queue",
			"Keep its own data in 1 storage area",
			"Manage 2 kinds of record",
		]);
	});

	it("points at the technical details when it cannot read one", () => {
		const fallback = ["The full request is under Technical details."];
		expect(requestSummary("{not json")).toEqual(fallback);
		expect(requestSummary('{"principal":"reports"}')).toEqual(fallback);
	});
});

describe("nextRevisionDraft", () => {
	it("starts from the current document with its version advanced", () => {
		expect(JSON.parse(nextRevisionDraft(current, BigInt(3))).revision).toBe(4);
		expect(JSON.parse(nextRevisionDraft(undefined, BigInt(0)))).toEqual({
			revision: 1,
			principals: {},
		});
	});
});
