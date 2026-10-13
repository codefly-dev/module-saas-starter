import { describe, expect, it } from "vitest";

import {
	encodeFrontendServicePath,
	frontendServiceTargetPath,
	isConnectProcedurePath,
	isFrontendServicePath,
} from "../src/transport.js";

// The four custom-verb shapes a module's bindings actually produce, as the
// Google HTTP form writes them: a collection verb, a sub-collection verb, a verb
// on an item identified by a path parameter, and a verb on a named resource.
// A parameter is already substituted by the time a path reaches the transport,
// so these are spelled with a value where the binding has `{id}`.
const CUSTOM_VERB_ROUTES: ReadonlyArray<readonly string[]> = [
	["records:recover"],
	["records", "record-1", "commands:recover"],
	["records", "record-1", "interactions", "interaction-1:respond"],
	["triggers", "nightly:fire"],
];

describe("relative plugin service paths", () => {
	it.each(CUSTOM_VERB_ROUTES.map((segments) => [segments.join("/"), segments]))(
		"carries the custom-verb route %s",
		(_label, segments) => {
			expect(isFrontendServicePath(segments)).toBe(true);
		},
	);

	it("carries plain reads and the reserved capability probe", () => {
		expect(isFrontendServicePath(["traffic"])).toBe(true);
		expect(isFrontendServicePath(["records", "record-1", "structure"])).toBe(true);
		expect(isFrontendServicePath([".well-known", "capabilities"])).toBe(true);
	});

	it.each([
		["an empty path", []],
		["an empty segment", ["records", ""]],
		["a dot segment", ["."]],
		["a traversal segment", ["records", "..", "secrets"]],
		["an encoded separator", ["a%2fb"]],
		["a literal separator", ["a/b"]],
		["a backslash", ["a\\b"]],
		["an oversized segment", ["a".repeat(257)]],
		// A colon is the custom-verb separator, so it is admitted exactly where
		// the form puts one and nowhere else. An interior colon would be a
		// resource whose name carries the separator, and two colons in one
		// segment is not a verb at all.
		["an interior colon", ["records:recover", "commands"]],
		["two colons", ["records:recover:again"]],
		["a colon with no resource", [":recover"]],
		["a colon with no verb", ["records:"]],
		["an upper-case verb", ["records:Recover"]],
		["a verb with a separator", ["records:recover-lost"]],
	])("refuses %s", (_label, segments) => {
		expect(isFrontendServicePath(segments)).toBe(false);
	});
});

describe("connect procedure paths", () => {
	it("names one procedure of a fully qualified service", () => {
		expect(
			isConnectProcedurePath(["example.records.v1.RecordService", "RecoverRecord"]),
		).toBe(true);
		expect(
			isConnectProcedurePath([
				"saas.frontend.plugin.v1.FrontendPluginCapabilityService",
				"GetFrontendPluginCapabilities",
			]),
		).toBe(true);
	});

	it.each([
		["a REST-shaped path", ["records", "record-1", "structure"]],
		["an unqualified service", ["RecordService", "RecoverRecord"]],
		["a missing method", ["example.records.v1.RecordService"]],
		["a trailing segment", ["example.records.v1.RecordService", "Recover", "x"]],
		["a custom verb", ["example.records.v1.RecordService", "records:recover"]],
		["a hyphenated service", ["example.records-v1.RecordService", "Recover"]],
	])("refuses %s", (_label, segments) => {
		expect(isConnectProcedurePath(segments)).toBe(false);
	});
});

describe("the path a declared binding lands on", () => {
	it("leaves a custom verb's colon literal", () => {
		expect(encodeFrontendServicePath(["records:recover"])).toBe("records:recover");
		expect(
			encodeFrontendServicePath([
				"records",
				"record-1",
				"interactions",
				"interaction-1:respond",
			]),
		).toBe("records/record-1/interactions/interaction-1:respond");
	});

	it("appends to the requirement's own prefix", () => {
		expect(
			frontendServiceTargetPath("/v1/example", ["records", "record-1:recover"]),
		).toBe("/v1/example/records/record-1:recover");
		expect(frontendServiceTargetPath("/v1/example", [])).toBe("/v1/example");
	});

	it("puts a root-prefixed requirement's path at the origin root", () => {
		expect(
			frontendServiceTargetPath("/", [
				"example.records.v1.RecordService",
				"RecoverRecord",
			]),
		).toBe("/example.records.v1.RecordService/RecoverRecord");
		expect(frontendServiceTargetPath("/", [])).toBe("/");
	});
});
